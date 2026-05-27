package cpi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// stubProvider is a minimal Provider implementation for registry tests.
// It satisfies the Provider interface but performs no real operations.
type stubProvider struct {
	name   string
	region string
}

func newStubProvider(name string) *stubProvider { return &stubProvider{name: name, region: "test"} }

func (s *stubProvider) Name() string   { return s.name }
func (s *stubProvider) Region() string { return s.region }

func (s *stubProvider) Authenticate(_ context.Context) error        { return nil }
func (s *stubProvider) ValidateCredentials(_ context.Context) error { return nil }

func (s *stubProvider) NetworkManager() NetworkManager         { return nil }
func (s *stubProvider) ComputeManager() ComputeManager         { return nil }
func (s *stubProvider) StorageManager() StorageManager         { return nil }
func (s *stubProvider) SecurityManager() SecurityManager       { return nil }
func (s *stubProvider) LoadBalancerManager() LoadBalancerManager { return nil }

func (s *stubProvider) Network() NetworkManager        { return nil }
func (s *stubProvider) Compute() ComputeManager        { return nil }
func (s *stubProvider) Storage() StorageManager        { return nil }
func (s *stubProvider) Security() SecurityManager      { return nil }
func (s *stubProvider) LoadBalancer() LoadBalancerManager { return nil }

func (s *stubProvider) SupportsStorage() bool { return false }

func (s *stubProvider) Initialize(_ context.Context, _ interface{}) error { return nil }
func (s *stubProvider) Cleanup(_ context.Context) error                   { return nil }

// makeFactory returns a ProviderFactory that always produces a stubProvider.
func makeFactory(name string) ProviderFactory {
	return func(_ interface{}) (Provider, error) {
		return newStubProvider(name), nil
	}
}

// registrySeq is a process-global counter that ensures provider names are
// unique even when tests run with -count=N (same binary, multiple runs).
var registrySeq atomic.Int64

// uniqueName generates a globally unique provider name for registry tests.
// The registry has no Unregister function; names persist for the process
// lifetime, so uniqueness across -count repetitions is required.
func uniqueName(t *testing.T, suffix string) string {
	t.Helper()
	seq := registrySeq.Add(1)
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return fmt.Sprintf("%s_%s_%d", safe, suffix, seq)
}

// ---- Register ----

func TestRegister_Success(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "ok")
	if err := Register(name, makeFactory(name)); err != nil {
		t.Fatalf("Register(%q) unexpected error: %v", name, err)
	}
}

func TestRegister_Duplicate(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "dup")
	if err := Register(name, makeFactory(name)); err != nil {
		t.Fatalf("first Register(%q) unexpected error: %v", name, err)
	}

	err := Register(name, makeFactory(name))
	if err == nil {
		t.Fatalf("second Register(%q) expected error, got nil", name)
	}

	if !strings.Contains(err.Error(), name) {
		t.Errorf("duplicate-register error %q should contain provider name %q", err.Error(), name)
	}
}

func TestRegister_EmptyName(t *testing.T) {
	t.Parallel()

	// Empty-string name is technically allowed by the current implementation
	// (the registry key is just the empty string). First call succeeds;
	// second call returns ErrProviderAlreadyRegistered("").
	// We test only that successive calls with "" return a duplicate error.
	// Because the empty-string slot may already be claimed by a prior run,
	// we accept either success-then-duplicate or duplicate-on-first call.
	err1 := Register("", makeFactory("empty"))
	err2 := Register("", makeFactory("empty"))

	if err1 == nil && err2 == nil {
		t.Error("at most one Register(\"\") should succeed; second must fail")
	}
}

// ---- Get ----

func TestGet_Registered(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "getok")
	if err := Register(name, makeFactory(name)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	factory, err := Get(name)
	if err != nil {
		t.Fatalf("Get(%q) unexpected error: %v", name, err)
	}

	if factory == nil {
		t.Fatal("Get returned nil factory")
	}

	// Verify the factory produces a Provider.
	p, err := factory(nil)
	if err != nil {
		t.Fatalf("factory() unexpected error: %v", err)
	}

	if p == nil {
		t.Fatal("factory returned nil provider")
	}
}

func TestGet_NotRegistered(t *testing.T) {
	t.Parallel()

	_, err := Get("this_provider_does_not_exist_xyz_12345")
	if err == nil {
		t.Fatal("Get on unknown provider should return error")
	}

	var pe *ProviderError
	if errors.As(err, &pe) {
		// should not be a ProviderError for missing provider (it's a plain fmt.Errorf)
		t.Logf("Got ProviderError (unexpected but accepted): %v", pe)
	}

	if !strings.Contains(err.Error(), "this_provider_does_not_exist_xyz_12345") {
		t.Errorf("error %q should contain the missing provider name", err.Error())
	}
}

// ---- List ----

func TestList_ContainsRegistered(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "list")
	if err := Register(name, makeFactory(name)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	names := List()
	found := false

	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("List() did not include registered provider %q; got %v", name, names)
	}
}

func TestList_ReturnsSlice(t *testing.T) {
	t.Parallel()

	// List must always return a non-nil slice (even when empty).
	names := List()
	if names == nil {
		t.Error("List() returned nil; want non-nil slice")
	}
}

// ---- GetProvider ----

func TestGetProvider_Success(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "getprovider")
	if err := Register(name, makeFactory(name)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p, err := GetProvider(name)
	if err != nil {
		t.Fatalf("GetProvider(%q) unexpected error: %v", name, err)
	}

	if p == nil {
		t.Fatal("GetProvider returned nil")
	}
}

func TestGetProvider_NotRegistered(t *testing.T) {
	t.Parallel()

	_, err := GetProvider("unregistered_provider_abc_99999")
	if err == nil {
		t.Fatal("GetProvider on unknown provider should return error")
	}
}

func TestGetProvider_FactoryError(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "factoryerr")
	factoryErr := errors.New("factory exploded")

	if err := Register(name, func(_ interface{}) (Provider, error) {
		return nil, factoryErr
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := GetProvider(name)
	if err == nil {
		t.Fatal("GetProvider should propagate factory error")
	}

	if !strings.Contains(err.Error(), "factory exploded") {
		t.Errorf("error %q should contain original factory error text", err.Error())
	}
}

// ---- CreateProvider ----

func TestCreateProvider_Success(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "createprovider")
	if err := Register(name, makeFactory(name)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx := context.Background()
	p, err := CreateProvider(ctx, name, nil)
	if err != nil {
		t.Fatalf("CreateProvider(%q) unexpected error: %v", name, err)
	}

	if p == nil {
		t.Fatal("CreateProvider returned nil")
	}
}

func TestCreateProvider_NotRegistered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := CreateProvider(ctx, "no_such_provider_zzzz", nil)

	if err == nil {
		t.Fatal("CreateProvider on unknown provider should return error")
	}
}

func TestCreateProvider_FactoryError(t *testing.T) {
	t.Parallel()

	name := uniqueName(t, "createfactoryerr")
	if err := Register(name, func(_ interface{}) (Provider, error) {
		return nil, fmt.Errorf("create factory failed")
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx := context.Background()
	_, err := CreateProvider(ctx, name, nil)

	if err == nil {
		t.Fatal("CreateProvider should propagate factory error")
	}

	if !strings.Contains(err.Error(), "create factory failed") {
		t.Errorf("error %q should contain factory error text", err.Error())
	}
}

// ---- Concurrent Register (no t.Parallel on outer test; goroutines do the concurrency) ----

// TestRegister_Concurrent verifies the registry is race-free when many
// goroutines register distinct providers simultaneously.
func TestRegister_Concurrent(t *testing.T) {
	const goroutines = 50

	var wg sync.WaitGroup

	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			seq := registrySeq.Add(1)
			name := fmt.Sprintf("conc_%s_%d_%d", t.Name(), i, seq)
			errs[i] = Register(name, makeFactory(name))
		}()
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Register returned unexpected error: %v", i, err)
		}
	}
}

// TestRegister_ConcurrentDuplicates spawns N goroutines all trying to register
// the same name. Exactly one must succeed; the rest must return
// ErrProviderAlreadyRegistered.
func TestRegister_ConcurrentDuplicates(t *testing.T) {
	const goroutines = 20

	name := uniqueName(t, "concdup")

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		successes int
		failures  int
	)

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Register(name, makeFactory(name))
			mu.Lock()
			if err == nil {
				successes++
			} else {
				failures++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	if successes != 1 {
		t.Errorf("expected exactly 1 successful Register for %q, got %d", name, successes)
	}

	if failures != goroutines-1 {
		t.Errorf("expected %d duplicate errors, got %d", goroutines-1, failures)
	}
}

// TestGet_ConcurrentReads verifies concurrent reads don't race against each other.
func TestGet_ConcurrentReads(t *testing.T) {
	t.Parallel()

	const readers = 50

	name := uniqueName(t, "concread")
	if err := Register(name, makeFactory(name)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var wg sync.WaitGroup

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Get(name)
		}()
	}

	wg.Wait()
}
