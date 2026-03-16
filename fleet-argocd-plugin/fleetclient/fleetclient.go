// Copyright 2024 Google LLC
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package fleetclient

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"text/template"
	"time"

	"fleet-management-tools/argocd-sync/protection"
	fleet "google.golang.org/api/gkehub/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// Fleet API service poll interval.
	reconcileInterval = 10 * time.Second
	// Template for the Kubernetes Secret name, {{.MembershipID}}.{{.Region}}.{{.ProjectNum}}.
	clusterSecretNameTemplate = "%s.%s.%s"
	// Template for the Kubernetes Secret manifest.
	clusterSecretTemplate = `
apiVersion: v1
kind: Secret
metadata:
  name: {{.Name}}
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
  annotations:
    fleet.gke.io/managed-by-fleet-plugin: "true"
type: Opaque
stringData:
  name: {{.Name}}
  server: {{.ConnectGatewayURL}}
  config: |
    {
      "execProviderConfig": {
        "command": "argocd-k8s-auth",
        "args": ["gcp"],
        "apiVersion": "client.authentication.k8s.io/v1beta1"
      },
      "tlsClientConfig": {
        "insecure": false,
        "caData": ""
      }
    }
`
)

// ProtectionConfig holds configuration for transient issue protection
type ProtectionConfig struct {
	MaxRetries           int
	RetryBaseDelay       time.Duration
	CacheMaxAge          time.Duration
	DetectionWindow      time.Duration
	OscillationThreshold int
	DropThreshold        float64
	DeletionGracePeriod  time.Duration
}

// clusterSecretTmpl is the parsed template for cluster secrets, parsed once at package init.
var clusterSecretTmpl = template.Must(template.New("secret").Parse(clusterSecretTemplate))

// FleetSync is a client that periodically polls the GKE Fleet API and caches fleet information.
type FleetSync struct {
	svc *fleet.Service
	// GCP project number of fleet host project.
	ProjectNum string
	// A cached map from Membership full resource name to a list of Scope IDs.
	MembershipTenancyMapCache map[string][]string
	// A cached map from Scope IDs to a list of Membership full resource names.
	ScopeTenancyMapCache map[string][]string

	// cacheMu protects MembershipTenancyMapCache and ScopeTenancyMapCache
	cacheMu sync.RWMutex

	// Reusable Kubernetes clientset, created once in NewFleetSync
	clientset kubernetes.Interface

	// Protection logic
	cache    *protection.Cache
	detector *protection.Detector
	config   *ProtectionConfig
}

// NewFleetSync creates a new FleetSync with protection logic and starts its periodical reconciliation.
func NewFleetSync(ctx context.Context, projectNum string, config *ProtectionConfig) (*FleetSync, error) {
	service, err := fleet.NewService(ctx)
	if err != nil {
		return nil, err
	}

	// Default configuration if not provided
	if config == nil {
		config = &ProtectionConfig{
			MaxRetries:           3,
			RetryBaseDelay:       2 * time.Second,
			CacheMaxAge:          60 * time.Minute,
			DetectionWindow:      10 * time.Minute,
			OscillationThreshold: 2,
			DropThreshold:        0.3,
			DeletionGracePeriod:  60 * time.Second,
		}
	}
	if config.DeletionGracePeriod == 0 {
		config.DeletionGracePeriod = 60 * time.Second
	}

	// Create Kubernetes clientset once for reuse across reconciliation cycles
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	c := &FleetSync{
		svc:        service,
		ProjectNum: projectNum,
		clientset:  clientset,
		cache:      protection.NewCache(config.CacheMaxAge),
		detector:   protection.NewDetector(config.DetectionWindow, config.OscillationThreshold, config.DropThreshold),
		config:     config,
	}

	// Build the initial fleet topology before handling RPCs.
	if err := c.Refresh(ctx); err != nil {
		return nil, err
	}

	c.startReconcile(ctx)
	return c, nil
}

func (c *FleetSync) startReconcile(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(reconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.Refresh(ctx); err != nil {
					fmt.Printf("Error refreshing fleet: %v\n", err)
				}
			case <-ctx.Done():
				log.Println("Reconciliation loop stopped")
				return
			}
		}
	}()
}

// Result encapsulates the response from the fleet service.
type Result struct {
	ServerURL string `json:"server"`
	Name      string `json:"name"`
	NameShort string `json:"nameShort"`
}

// PluginResults returns the results of the plugin.
func (c *FleetSync) PluginResults(ctx context.Context, scopeID string) ([]Result, error) {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()

	if c.MembershipTenancyMapCache == nil || c.ScopeTenancyMapCache == nil {
		return nil, fmt.Errorf("fleet is empty")
	}
	var results []Result

	// Scope mode. Only include memberships in the specified scope.
	if scopeID != "" {
		if c.ScopeTenancyMapCache[scopeID] == nil {
			return nil, fmt.Errorf("unknown scope ID to the Fleet plugin: %s", scopeID)
		}
		for _, name := range c.ScopeTenancyMapCache[scopeID] {
			results = append(results, resultFromMembership(name, c.ProjectNum))
		}
		return results, nil
	}

	// Include all member clusters in the Fleet.
	for name := range c.MembershipTenancyMapCache {
		results = append(results, resultFromMembership(name, c.ProjectNum))
	}
	return results, nil
}

func resultFromMembership(name, projectNum string) Result {
	parts := strings.Split(name, "/")
	region, membershipID := parts[3], parts[5]
	return Result{
		ServerURL: connectGatewayURL(projectNum, region, membershipID),
		Name:      fmt.Sprintf(clusterSecretNameTemplate, membershipID, region, projectNum),
		NameShort: fmt.Sprint(membershipID),
	}
}

func connectGatewayURL(projectNum, region, membershipID string) string {
	if region == "global" {
		return fmt.Sprintf("https://connectgateway.googleapis.com/v1/projects/%s/locations/%s/gkeMemberships/%s", projectNum, region, membershipID)
	}
	return fmt.Sprintf("https://%s-connectgateway.googleapis.com/v1/projects/%s/locations/%s/gkeMemberships/%s", region, projectNum, region, membershipID)
}

// Refresh polls fleet API, rebuilds the local cached fleet topology map, and updates cluster secrets.
func (c *FleetSync) Refresh(ctx context.Context) error {
	mems, err := c.listMemberships(ctx, c.ProjectNum)
	if err != nil {
		return fmt.Errorf("failed to list memberships: %w", err)
	}

	scopes, err := c.listScopes(ctx, c.ProjectNum)
	if err != nil {
		return fmt.Errorf("failed to list scopes: %w", err)
	}

	mbs, err := c.listMembershipBindings(ctx, c.ProjectNum)
	if err != nil {
		return fmt.Errorf("failed to list membership bindings: %w", err)
	}

	// Build one map from Memberships to a list of Scopes that the membership cluster is associated with,
	// and one reverse indexed map from Scopes to Memberships.
	memTenancyMap := make(map[string][]string)
	for _, mem := range mems {
		membershipName := mem.Name
		memTenancyMap[membershipName] = make([]string, 0)
	}

	scopeTenancyMap := make(map[string][]string)
	for _, s := range scopes {
		scopeID := s.Name
		scopeTenancyMap[scopeID] = make([]string, 0)
	}

	for _, binding := range mbs {
		// bindingName is in the format of
		// `projects/{project}/locations/{location}/memberships/{membership}/bindings/{membershipbinding}`
		bindingName := binding.Name
		parts := strings.Split(bindingName, "/")
		if len(parts) != 8 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "memberships" || parts[6] != "bindings" {
			fmt.Printf("Invalid binding resource name format: %s\n", bindingName)
			continue
		}

		// Add the scope to the list for this membership
		membership := strings.Join(parts[:6], "/")
		scopeParts := strings.Split(binding.Scope, "/")
		if len(scopeParts) == 0 {
			fmt.Printf("Invalid scope in binding (%s): %s\n", bindingName, binding.Scope)
			continue
		}

		scope := scopeParts[len(scopeParts)-1]
		memTenancyMap[membership] = append(memTenancyMap[membership], scope)
		scopeTenancyMap[scope] = append(scopeTenancyMap[scope], membership)
	}

	// Refresh cache under write lock.
	c.cacheMu.Lock()
	c.MembershipTenancyMapCache = memTenancyMap
	c.ScopeTenancyMapCache = scopeTenancyMap
	c.cacheMu.Unlock()

	// Update cluster Secrets.
	if err := c.reconcileClusterSecrets(ctx); err != nil {
		return fmt.Errorf("failed to reconcile cluster secrets: %w", err)
	}
	return nil
}

func (c *FleetSync) reconcileClusterSecrets(ctx context.Context) error {
	c.cacheMu.RLock()
	// Construct a map of cluster secrets, from name to manifest.
	clusterSecrets := make(map[string]string)
	for membership := range c.MembershipTenancyMapCache {
		parts := strings.Split(membership, "/")
		secretName := fmt.Sprintf(clusterSecretNameTemplate, parts[5], parts[3], c.ProjectNum)
		param := struct {
			Name              string
			ConnectGatewayURL string
		}{
			Name:              secretName,
			ConnectGatewayURL: connectGatewayURL(c.ProjectNum, parts[3], parts[5]),
		}
		var secretManifest bytes.Buffer
		err := clusterSecretTmpl.Execute(&secretManifest, param)
		if err != nil {
			fmt.Println("Error creating Secret manifest:", err)
			continue
		}
		clusterSecrets[secretName] = secretManifest.String()
	}
	c.cacheMu.RUnlock()

	fmt.Printf("Reconciling Cluster Secrets: %v\n", clusterSecrets)

	// Apply the Secret to the cluster.
	if err := applySecrets(ctx, c.clientset, clusterSecrets); err != nil {
		return fmt.Errorf("failed to apply secret: %w", err)
	}

	// Prune cluster secrets that are no longer existing in the Fleet.
	return pruneSecrets(ctx, c.clientset, clusterSecrets, c.config.DeletionGracePeriod)
}

func applySecrets(ctx context.Context, clientset kubernetes.Interface, clusterSecrets map[string]string) error {
	secretsClient := clientset.CoreV1().Secrets("argocd")
	for _, manifest := range clusterSecrets {
		secret, err := secretFromManifest(manifest)
		if err != nil {
			return fmt.Errorf("error converting manifest %q to a k8s secret: %v", manifest, err)
		}
		_, err = secretsClient.Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			// Check if "already exists", then update.
			if !errors.IsAlreadyExists(err) {
				return fmt.Errorf("error creating secret: %v", err)
			}
			_, err = secretsClient.Update(ctx, secret, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("error updating secret: %v", err)
			}
		}
	}
	fmt.Println("Successfully applied Secrets.")
	return nil
}

func pruneSecrets(ctx context.Context, clientset kubernetes.Interface, clusterSecrets map[string]string, gracePeriod time.Duration) error {
	secretsClient := clientset.CoreV1().Secrets("argocd")
	existingSecrets, err := secretsClient.List(ctx, metav1.ListOptions{
		LabelSelector: "argocd.argoproj.io/secret-type=cluster",
	})
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	// Warning when many secrets absent
	fleetManaged, absent := 0, 0
	for _, secret := range existingSecrets.Items {
		if secret.Annotations["fleet.gke.io/managed-by-fleet-plugin"] != "true" {
			continue
		}
		fleetManaged++
		if _, exists := clusterSecrets[secret.Name]; !exists {
			absent++
		}
	}
	if fleetManaged > 0 && float64(absent)/float64(fleetManaged) > 0.3 {
		log.Printf("CRITICAL WARNING: %d/%d fleet secrets (%.0f%%) absent — possible Fleet API issue",
			absent, fleetManaged, float64(absent)/float64(fleetManaged)*100)
	}

	for _, secret := range existingSecrets.Items {
		if secret.Annotations["fleet.gke.io/managed-by-fleet-plugin"] != "true" {
			continue
		}

		_, stillExists := clusterSecrets[secret.Name]
		absentSince := secret.Annotations["fleet.gke.io/absent-since"]

		if stillExists {
			// Membership is present — remove absent-since annotation if it was set
			if absentSince != "" {
				log.Printf("Membership recovered for secret %s, removing absent-since annotation", secret.Name)
				delete(secret.Annotations, "fleet.gke.io/absent-since")
				if _, err := secretsClient.Update(ctx, &secret, metav1.UpdateOptions{}); err != nil {
					return fmt.Errorf("failed to update secret %s: %w", secret.Name, err)
				}
			}
			continue
		}

		// Membership absent — two-phase deletion
		if absentSince == "" {
			log.Printf("Membership absent for secret %s — marking (grace period: %v)", secret.Name, gracePeriod)
			if secret.Annotations == nil {
				secret.Annotations = make(map[string]string)
			}
			secret.Annotations["fleet.gke.io/absent-since"] = time.Now().Format(time.RFC3339)
			if _, err := secretsClient.Update(ctx, &secret, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("failed to update secret %s: %w", secret.Name, err)
			}
			continue
		}

		markedTime, err := time.Parse(time.RFC3339, absentSince)
		if err != nil {
			log.Printf("Invalid absent-since on %s, resetting: %v", secret.Name, err)
			secret.Annotations["fleet.gke.io/absent-since"] = time.Now().Format(time.RFC3339)
			if _, err := secretsClient.Update(ctx, &secret, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("failed to update secret %s: %w", secret.Name, err)
			}
			continue
		}

		if elapsed := time.Since(markedTime); elapsed < gracePeriod {
			log.Printf("Secret %s absent for %v / %v — waiting", secret.Name, elapsed.Round(time.Second), gracePeriod)
			continue
		}

		log.Printf("Secret %s absent beyond grace period — deleting", secret.Name)
		if err := secretsClient.Delete(ctx, secret.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("failed to delete secret %s: %w", secret.Name, err)
		}
	}

	fmt.Println("Successfully pruned Secrets.")
	return nil
}

func secretFromManifest(manifest string) (*corev1.Secret, error) {
	// Universal deserializer can handle various Kubernetes object formats
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding to scheme: %v", err)
	}

	decode := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode
	obj, _, err := decode([]byte(manifest), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("error decoding manifest %q: %v", manifest, err)
	}
	// Type assertion to ensure it's a corev1.Secret
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil, fmt.Errorf("decoded object is not of type Secret")
	}
	return secret, nil
}

// listMemberships fetches the memberships under a given parent.
func (c *FleetSync) listMemberships(ctx context.Context, project string) ([]*fleet.Membership, error) {
	var ret []*fleet.Membership
	parent := fmt.Sprintf("projects/%s/locations/-", project)
	call := c.svc.Projects.Locations.Memberships.List(parent)
	err := call.Pages(ctx, func(resp *fleet.ListMembershipsResponse) error {
		ret = append(ret, resp.Resources...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// listScopes fetches the scopes under a given parent.
func (c *FleetSync) listScopes(ctx context.Context, project string) ([]*fleet.Scope, error) {
	var ret []*fleet.Scope
	parent := fmt.Sprintf("projects/%s/locations/global", project)
	call := c.svc.Projects.Locations.Scopes.List(parent)
	err := call.Pages(ctx, func(resp *fleet.ListScopesResponse) error {
		ret = append(ret, resp.Scopes...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// listMembershipBindings fetches the membership bindings under a given parent.
// listMembershipBindings fetches the membership bindings with transient issue protection.
func (c *FleetSync) listMembershipBindings(ctx context.Context, project string) ([]*fleet.MembershipBinding, error) {
	for attempt := 0; attempt < c.config.MaxRetries; attempt++ {
		// Call Fleet API
		bindings, err := c.listMembershipBindingsInternal(ctx, project)
		if err != nil {
			if attempt < c.config.MaxRetries-1 {
				delay := c.config.RetryBaseDelay * time.Duration(math.Pow(2, float64(attempt)))
				log.Printf("Fleet API error (attempt %d/%d), retrying in %v: %v",
					attempt+1, c.config.MaxRetries, delay, err)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}

			// All retries failed, try cache
			if cached, ok := c.cache.Get(); ok {
				log.Printf("Fleet API failed after %d attempts, using cached response (age: %v)",
					c.config.MaxRetries, c.cache.Age())
				return cached, nil
			}

			return nil, fmt.Errorf("fleet API failed after %d attempts and no valid cache: %w",
				c.config.MaxRetries, err)
		}

		// Check for transient issue
		itemCount := len(bindings)
		if isTransient, reason := c.detector.IsTransientIssue(itemCount); isTransient {
			log.Printf("Transient issue detected: %s", reason)

			if attempt < c.config.MaxRetries-1 {
				delay := c.config.RetryBaseDelay * time.Duration(math.Pow(2, float64(attempt)))
				log.Printf("Retrying Fleet API (attempt %d/%d) in %v", attempt+1, c.config.MaxRetries, delay)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}

			// Retries exhausted, use cache
			if cached, ok := c.cache.Get(); ok {
				log.Printf("Transient issue persists after %d attempts, using cached response (age: %v)",
					c.config.MaxRetries, c.cache.Age())
				return cached, nil
			}

			// Fix: refuse to return suspicious data when cache is unavailable
			return nil, fmt.Errorf("transient issue detected (%s) after %d attempts with no valid cache: refusing suspicious data",
				reason, c.config.MaxRetries)
		}

		// Response looks good, cache it
		c.cache.Set(bindings)
		log.Printf("Fleet API success: %d membership bindings", itemCount)
		return bindings, nil
	}

	return nil, fmt.Errorf("unexpected retry loop exit")
}

// listMembershipBindingsInternal is the original implementation (renamed from listMembershipBindings)
func (c *FleetSync) listMembershipBindingsInternal(ctx context.Context, project string) ([]*fleet.MembershipBinding, error) {
	var ret []*fleet.MembershipBinding
	parent := fmt.Sprintf("projects/%s/locations/-/memberships/-", project)
	call := c.svc.Projects.Locations.Memberships.Bindings.List(parent)
	err := call.Pages(ctx, func(resp *fleet.ListMembershipBindingsResponse) error {
		ret = append(ret, resp.MembershipBindings...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}
