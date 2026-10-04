package config

// Sections whose types, defaults and validation live in files of their own
// (audit.go, approval.go, write_anomaly.go, self_heal.go, quota.go), wired
// into Load and Validate here so config.go does not grow with each one.

func setSectionDefaults() {
	setAuditDefaults()
	setApprovalDefaults()
	setWriteAnomalyDefaults()
	setSelfHealDefaults()
	setQuotaDefaults()
}

func (c *Config) validateSections() error {
	for _, validate := range []func() error{
		c.Audit.validate,
		func() error { return c.RBAC.Approval.validate(c.RBAC.Enabled) },
		c.Alert.WriteAnomaly.validate,
		c.SelfHeal.validate,
		c.Quota.validate,
	} {
		if err := validate(); err != nil {
			return err
		}
	}
	return nil
}
