package vault

import (
	"testing"
)

func TestBuildVaultTree_EmptyInput(t *testing.T) {
	m := &Manager{}

	tree, err := m.buildTree([]string{})
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	if tree == nil {
		t.Fatal("tree should not be nil")
	}
	if tree.Root == nil {
		t.Fatal("tree.Root should not be nil")
	}
	if tree.Root.Name != "secret" {
		t.Errorf("root name = %q, want 'secret'", tree.Root.Name)
	}
	if len(tree.Root.Children) != 0 {
		t.Errorf("root should have no children, got %d", len(tree.Root.Children))
	}
	if len(tree.Root.Keys) != 0 {
		t.Errorf("root should have no keys, got %d", len(tree.Root.Keys))
	}
}

func TestBuildVaultTree_SinglePath(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"config/bosh:admin_password",
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Verify root
	if tree.Root.Name != "secret" {
		t.Errorf("root name = %q, want 'secret'", tree.Root.Name)
	}

	// Verify first level (config)
	configNode, ok := tree.Root.Children["config"]
	if !ok {
		t.Fatal("root should have 'config' child")
	}
	if configNode.Name != "config" {
		t.Errorf("config node name = %q, want 'config'", configNode.Name)
	}
	if configNode.FullPath != "secret/config" {
		t.Errorf("config fullPath = %q, want 'secret/config'", configNode.FullPath)
	}

	// Verify second level (bosh)
	boshNode, ok := configNode.Children["bosh"]
	if !ok {
		t.Fatal("config should have 'bosh' child")
	}
	if boshNode.Name != "bosh" {
		t.Errorf("bosh node name = %q, want 'bosh'", boshNode.Name)
	}
	if boshNode.FullPath != "secret/config/bosh" {
		t.Errorf("bosh fullPath = %q, want 'secret/config/bosh'", boshNode.FullPath)
	}

	// Verify key
	if len(boshNode.Keys) != 1 {
		t.Fatalf("bosh node should have 1 key, got %d", len(boshNode.Keys))
	}
	if boshNode.Keys[0] != "admin_password" {
		t.Errorf("key = %q, want 'admin_password'", boshNode.Keys[0])
	}
}

func TestBuildVaultTree_NestedPaths(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"config/scf/certs:domains",
		"config/scf/certs:provider",
		"config/scf/mgmt/bosh/blobstores/bosh:access_key_id",
		"config/scf/mgmt/bosh/blobstores/bosh:host",
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Navigate to certs node
	config := tree.Root.Children["config"]
	if config == nil {
		t.Fatal("missing 'config' node")
	}

	scf := config.Children["scf"]
	if scf == nil {
		t.Fatal("missing 'scf' node")
	}

	certs := scf.Children["certs"]
	if certs == nil {
		t.Fatal("missing 'certs' node")
	}

	// Verify certs has 2 keys
	if len(certs.Keys) != 2 {
		t.Errorf("certs should have 2 keys, got %d", len(certs.Keys))
	}

	// Navigate to bosh node
	mgmt := scf.Children["mgmt"]
	if mgmt == nil {
		t.Fatal("missing 'mgmt' node")
	}

	bosh := mgmt.Children["bosh"]
	if bosh == nil {
		t.Fatal("missing 'bosh' node")
	}

	blobstores := bosh.Children["blobstores"]
	if blobstores == nil {
		t.Fatal("missing 'blobstores' node")
	}

	boshLeaf := blobstores.Children["bosh"]
	if boshLeaf == nil {
		t.Fatal("missing 'bosh' leaf node")
	}

	// Verify bosh leaf has 2 keys
	if len(boshLeaf.Keys) != 2 {
		t.Errorf("bosh leaf should have 2 keys, got %d", len(boshLeaf.Keys))
	}

	// Verify full path
	if boshLeaf.FullPath != "secret/config/scf/mgmt/bosh/blobstores/bosh" {
		t.Errorf("bosh leaf fullPath = %q", boshLeaf.FullPath)
	}
}

func TestBuildVaultTree_MultipleKeysAtSamePath(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"database/postgres:username",
		"database/postgres:password",
		"database/postgres:host",
		"database/postgres:port",
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	database := tree.Root.Children["database"]
	if database == nil {
		t.Fatal("missing 'database' node")
	}

	postgres := database.Children["postgres"]
	if postgres == nil {
		t.Fatal("missing 'postgres' node")
	}

	if len(postgres.Keys) != 4 {
		t.Errorf("postgres should have 4 keys, got %d", len(postgres.Keys))
	}

	// Verify all keys are present
	expectedKeys := map[string]bool{
		"username": false,
		"password": false,
		"host":     false,
		"port":     false,
	}

	for _, key := range postgres.Keys {
		if _, ok := expectedKeys[key]; ok {
			expectedKeys[key] = true
		}
	}

	for key, found := range expectedKeys {
		if !found {
			t.Errorf("key %q not found in tree", key)
		}
	}
}

func TestBuildVaultTree_RootLevelKeys(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		":global_key1",
		":global_key2",
		"config/nested:nested_key",
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Root level keys should be in root node
	if len(tree.Root.Keys) != 2 {
		t.Errorf("root should have 2 keys, got %d", len(tree.Root.Keys))
	}

	// Verify nested path still works
	config := tree.Root.Children["config"]
	if config == nil {
		t.Fatal("missing 'config' node")
	}

	nested := config.Children["nested"]
	if nested == nil {
		t.Fatal("missing 'nested' node")
	}

	if len(nested.Keys) != 1 {
		t.Errorf("nested should have 1 key, got %d", len(nested.Keys))
	}
}

func TestBuildVaultTree_MalformedEntries(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"config/valid:key1",
		"malformed_no_colon", // Should be skipped
		"config/valid:key2",
		":multiple:colons:here", // Takes first two parts
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Verify valid entries were processed
	config := tree.Root.Children["config"]
	if config == nil {
		t.Fatal("missing 'config' node")
	}

	valid := config.Children["valid"]
	if valid == nil {
		t.Fatal("missing 'valid' node")
	}

	if len(valid.Keys) != 2 {
		t.Errorf("valid node should have 2 keys, got %d", len(valid.Keys))
	}
}

func TestBuildVaultTree_EmptySegments(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"config//double/slash:key1", // Double slash should be handled
		"/leading/slash:key2",       // Leading slash
		"trailing/slash/:key3",      // Trailing slash before colon
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Tree should still be built, empty segments should be skipped
	if tree.Root == nil {
		t.Fatal("tree.Root should not be nil")
	}

	// Verify structure exists (implementation may vary on empty segment handling)
	if len(tree.Root.Children) == 0 {
		t.Error("tree should have some children despite empty segments")
	}
}

func TestBuildVaultTree_DeepNesting(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"level1/level2/level3/level4/level5/level6:deep_key",
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Navigate through all levels
	current := tree.Root
	levels := []string{"level1", "level2", "level3", "level4", "level5", "level6"}

	for i, levelName := range levels {
		if current.Children[levelName] == nil {
			t.Fatalf("missing node at level %d: %s", i+1, levelName)
		}
		current = current.Children[levelName]
	}

	// Verify final node has the key
	if len(current.Keys) != 1 {
		t.Errorf("deepest node should have 1 key, got %d", len(current.Keys))
	}
	if current.Keys[0] != "deep_key" {
		t.Errorf("key = %q, want 'deep_key'", current.Keys[0])
	}

	// Verify full path
	expectedPath := "secret/level1/level2/level3/level4/level5/level6"
	if current.FullPath != expectedPath {
		t.Errorf("fullPath = %q, want %q", current.FullPath, expectedPath)
	}
}

func TestBuildVaultTree_OverlappingPaths(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"a/b/c:key1",
		"a/b:key2",
		"a:key3",
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Verify 'a' has key3
	a := tree.Root.Children["a"]
	if a == nil {
		t.Fatal("missing 'a' node")
	}
	if len(a.Keys) != 1 || a.Keys[0] != "key3" {
		t.Error("'a' should have key3")
	}

	// Verify 'a/b' has key2
	b := a.Children["b"]
	if b == nil {
		t.Fatal("missing 'b' node")
	}
	if len(b.Keys) != 1 || b.Keys[0] != "key2" {
		t.Error("'b' should have key2")
	}

	// Verify 'a/b/c' has key1
	c := b.Children["c"]
	if c == nil {
		t.Fatal("missing 'c' node")
	}
	if len(c.Keys) != 1 || c.Keys[0] != "key1" {
		t.Error("'c' should have key1")
	}
}

func TestBuildVaultTree_ChildrenMapInitialization(t *testing.T) {
	m := &Manager{}

	pathsWithKeys := []string{
		"path1:key1",
		"path2:key2",
	}

	tree, err := m.buildTree(pathsWithKeys)
	if err != nil {
		t.Fatalf("buildTree failed: %v", err)
	}

	// Verify all nodes have initialized maps
	if tree.Root.Children == nil {
		t.Error("root.Children should be initialized")
	}

	for _, child := range tree.Root.Children {
		if child.Children == nil {
			t.Errorf("child %q should have initialized Children map", child.Name)
		}
		if child.Keys == nil {
			t.Errorf("child %q should have initialized Keys slice", child.Name)
		}
	}
}
