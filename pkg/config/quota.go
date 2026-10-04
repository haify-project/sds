package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// QuotaConfig is [quota]: limits on how much storage may be promised
// (pkg/controller/quota.go).
//
// MaxOvercommitRatio caps a thin pool's virtual size — the sum of its thin
// volumes' sizes — at that many times its real size; 0 leaves thin pools
// unlimited, as they always were. A new replica or a resize that would cross
// it is refused on that node, and auto-placement passes such a node over.
//
// Projects caps the total size and count of the resources labelled
// ProjectLabel=<name> (sizes counted once, not per replica).
type QuotaConfig struct {
	MaxOvercommitRatio float64        `mapstructure:"max_overcommit_ratio"`
	ProjectLabel       string         `mapstructure:"project_label"`
	Projects           []ProjectQuota `mapstructure:"projects"`
}

// ProjectQuota is one project's limits; zero means no limit.
type ProjectQuota struct {
	Name         string `mapstructure:"name"`
	MaxGB        uint64 `mapstructure:"max_gb"`
	MaxResources int    `mapstructure:"max_resources"`
}

func setQuotaDefaults() {
	viper.SetDefault("quota.max_overcommit_ratio", 0.0)
	viper.SetDefault("quota.project_label", "project")
}

// Project returns the quota for name, or nil.
func (q QuotaConfig) Project(name string) *ProjectQuota {
	for i := range q.Projects {
		if q.Projects[i].Name == name {
			return &q.Projects[i]
		}
	}
	return nil
}

func (q *QuotaConfig) validate() error {
	if q.MaxOvercommitRatio < 0 || (q.MaxOvercommitRatio > 0 && q.MaxOvercommitRatio < 1) {
		return fmt.Errorf("quota.max_overcommit_ratio must be 0 (unlimited) or at least 1")
	}
	if q.ProjectLabel == "" {
		q.ProjectLabel = "project"
	}
	seen := map[string]bool{}
	for _, p := range q.Projects {
		if p.Name == "" || seen[p.Name] {
			return fmt.Errorf("quota.projects: every project needs a unique name")
		}
		seen[p.Name] = true
		if p.MaxResources < 0 {
			return fmt.Errorf("quota.projects %q: max_resources must not be negative", p.Name)
		}
	}
	return nil
}
