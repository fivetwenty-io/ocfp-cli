package vault

// SafeInterface defines the interface for vault safe operations.
// This allows for mocking in tests.
//nolint:interfacebloat // Safe abstraction intentionally aggregates many operations
type SafeInterface interface {
	Set(path, key string, value interface{}) error
	SetMultiple(path string, data map[string]interface{}) error
	Get(path, key string) (interface{}, error)
	GetAll(path string) (map[string]interface{}, error)
	Exists(path string) (bool, error)
	Delete(path, key string) error
	List(path string) ([]string, error)
	Export(path string) (map[string]interface{}, error)
	Import(path string, data map[string]interface{}) error
	GetEngineInfo(path string) (*EngineInfo, error)
	MustGet(path, key string) interface{}
	GetString(path, key string) (string, error)
	GetJSON(path, key string) ([]byte, error)
}

// Ensure Safe implements SafeInterface.
var _ SafeInterface = (*Safe)(nil)
