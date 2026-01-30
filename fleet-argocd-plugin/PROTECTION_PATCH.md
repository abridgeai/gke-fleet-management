# Protection Logic - Code Changes Required

## File: `fleetclient/fleetclient.go`

### Change 1: Add imports (after line 14)

```go
import (
	"bytes"
	"context"
	"fmt"
	"math"      // ADD THIS
	"strings"
	"text/template"
	"time"

	"fleet-management-tools/argocd-sync/fleetclient/protection"  // ADD THIS
	fleet "google.golang.org/api/gkehub/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)
```

### Change 2: Add fields to FleetSync struct (after line 69)

```go
// FleetSync is a client that periodically polls the GKE Fleet API and caches fleet information.
type FleetSync struct {
	svc *fleet.Service
	// GCP project number of fleet host project.
	ProjectNum string
	// A cached map from Membership full resource name to a list of Scope IDs.
	MembershipTenancyMapCache map[string][]string
	// A cached map from Scope IDs to a list of Membership full resource names.
	ScopeTenancyMapCache map[string][]string

	// Protection logic
	cache    *protection.Cache     // ADD THIS
	detector *protection.Detector  // ADD THIS
	config   *ProtectionConfig     // ADD THIS
}

// ProtectionConfig holds configuration for transient issue protection
type ProtectionConfig struct {
	MaxRetries           int
	RetryBaseDelay       time.Duration
	CacheMaxAge          time.Duration
	DetectionWindow      time.Duration
	OscillationThreshold int
	DropThreshold        float64
}
```

### Change 3: Update NewFleetSync to initialize protection (replace function at line 80)

```go
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
		}
	}

	c := &FleetSync{
		svc:        service,
		ProjectNum: projectNum,
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
```

### Change 4: Replace listMembershipBindings with protected version (replace function at line 374)

```go
// listMembershipBindings fetches the membership bindings with transient issue protection.
func (c *FleetSync) listMembershipBindings(ctx context.Context, project string) ([]*fleet.MembershipBinding, error) {
	for attempt := 0; attempt < c.config.MaxRetries; attempt++ {
		// Call Fleet API
		bindings, err := c.listMembershipBindingsInternal(ctx, project)
		if err != nil {
			if attempt < c.config.MaxRetries-1 {
				delay := c.config.RetryBaseDelay * time.Duration(math.Pow(2, float64(attempt)))
				log.Printf("⚠️  Fleet API error (attempt %d/%d), retrying in %v: %v",
					attempt+1, c.config.MaxRetries, delay, err)
				time.Sleep(delay)
				continue
			}

			// All retries failed, try cache
			if cached, ok := c.cache.Get(); ok {
				log.Printf("🔥 ERROR: Fleet API failed after %d attempts, using cached response (age: %v)",
					c.config.MaxRetries, c.cache.Age())
				return cached, nil
			}

			return nil, fmt.Errorf("fleet API failed after %d attempts and no valid cache: %w",
				c.config.MaxRetries, err)
		}

		// Check for transient issue
		itemCount := len(bindings)
		if isTransient, reason := c.detector.IsTransientIssue(itemCount); isTransient {
			log.Printf("⚠️  Transient issue detected: %s", reason)

			if attempt < c.config.MaxRetries-1 {
				delay := c.config.RetryBaseDelay * time.Duration(math.Pow(2, float64(attempt)))
				log.Printf("🔄 Retrying Fleet API (attempt %d/%d) in %v", attempt+1, c.config.MaxRetries, delay)
				time.Sleep(delay)
				continue
			}

			// Retries exhausted, use cache
			if cached, ok := c.cache.Get(); ok {
				log.Printf("🔥 ERROR: Transient issue persists after %d attempts, using cached response (age: %v)",
					c.config.MaxRetries, c.cache.Age())
				return cached, nil
			}

			log.Printf("🔥 CRITICAL: Transient issue detected but no valid cache, returning incomplete data")
			// You could return error here to block reconciliation entirely
		}

		// Response looks good, cache it
		c.cache.Set(bindings)
		log.Printf("✅ Fleet API success: %d membership bindings", itemCount)
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
```

## File: `main.go`

### Change 1: Update imports (after line 16)

```go
import (
	"context"
	"encoding/json"
	"fleet-management-tools/argocd-sync/fleetclient"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"     // ADD THIS
	"time"        // ADD THIS
)
```

### Change 2: Add getProtectionConfig function (before main function)

```go
// getProtectionConfig reads protection configuration from environment variables
func getProtectionConfig() *fleetclient.ProtectionConfig {
	return &fleetclient.ProtectionConfig{
		MaxRetries:           getEnvInt("MAX_API_RETRIES", 3),
		RetryBaseDelay:       time.Duration(getEnvInt("RETRY_BASE_DELAY_SECONDS", 2)) * time.Second,
		CacheMaxAge:          time.Duration(getEnvInt("CACHE_MAX_AGE_MINUTES", 60)) * time.Minute,
		DetectionWindow:      time.Duration(getEnvInt("DETECTION_WINDOW_MINUTES", 10)) * time.Minute,
		OscillationThreshold: getEnvInt("OSCILLATION_THRESHOLD", 2),
		DropThreshold:        float64(getEnvInt("DROP_THRESHOLD_PERCENT", 30)) / 100.0,
	}
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}
```

### Change 3: Update main function (line 29)

```go
func main() {
	log.Println("Starting GKE Fleet argocd plugin with transient issue protection...")
	projectNum := os.Getenv("FLEET_PROJECT_NUMBER")
	if projectNum == "" {
		log.Fatal("ENV var FLEET_PROJECT_NUMBER not found")
	}
	portNum := os.Getenv("PORT")
	if portNum == "" {
		log.Fatal("ENV var PORT not found")
	}

	// Get protection configuration
	protectionConfig := getProtectionConfig()
	log.Printf("Protection config: MaxRetries=%d, CacheMaxAge=%v, DetectionWindow=%v, OscillationThreshold=%d, DropThreshold=%.0f%%",
		protectionConfig.MaxRetries,
		protectionConfig.CacheMaxAge,
		protectionConfig.DetectionWindow,
		protectionConfig.OscillationThreshold,
		protectionConfig.DropThreshold*100,
	)

	// Start fleet client with protection
	ctx := context.Background()
	var err error
	fleetSync, err = fleetclient.NewFleetSync(ctx, projectNum, protectionConfig)
	if err != nil {
		fmt.Printf("Error creating fleet client: %v\n", err)
		log.Fatal(err)
	}

	http.HandleFunc("/api/v1/getparams.execute", Reply)
	// Spinning up the server.
	log.Println("Started on port", portNum)
	fmt.Println("To close connection CTRL+C :-)")
	err = http.ListenAndServe(portNum, nil)
	if err != nil {
		log.Fatal(err)
	}
}
```

## File: `go.mod`

No changes required - protection package uses only standard library and existing dependencies.

## Testing the Changes

1. Build locally:
```bash
cd fleet-argocd-plugin
go mod tidy
go build -o fleet-sync .
```

2. Run tests (create tests later):
```bash
go test ./...
```

3. Build Docker image:
```bash
docker build -t fleet-argocd-plugin:test .
```

4. Test with environment variables:
```bash
docker run -e FLEET_PROJECT_NUMBER=test \
  -e PORT=:4356 \
  -e MAX_API_RETRIES=3 \
  -e CACHE_MAX_AGE_MINUTES=60 \
  fleet-argocd-plugin:test
```
