package config

import "testing"

func TestArtifactsCloudConfiguration(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		cfg := &Config{Provider: provider, Bastion: Bastion{Image: "ubuntu-image", Flavor: "native-size"}, Artifacts: ArtifactsConfig{Enabled: true}}
		if err := applyDefaults(cfg, provider); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Artifacts.Validate(provider, true, true); err != nil {
			t.Errorf("%s: %v", provider, err)
		}
		if cfg.Artifacts.Template != "ubuntu-image" {
			t.Errorf("%s template=%s", provider, cfg.Artifacts.Template)
		}
		if cfg.Artifacts.Data.StoragePool == "local-zfs" {
			t.Errorf("%s uses PVE storage", provider)
		}
		if cfg.Artifacts.Flavor == "artifacts" {
			t.Errorf("%s uses PVE flavor", provider)
		}
	}
}

func TestCloudArtifactsRejectsPVESizingAndImage(t *testing.T) {
	a := ArtifactsConfig{Enabled: true}
	a.Defaults()
	if err := a.Validate("aws", true, true); err == nil {
		t.Fatal("must require a cloud image")
	}
	a.Template = "ami-example"
	a.CPU = 4
	if err := a.Validate("aws", true, true); err == nil {
		t.Fatal("cloud sizing must use native flavor")
	}
}
