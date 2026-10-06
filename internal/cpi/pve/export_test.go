package pve

// ParsePVERule and BuildPVERuleParams expose the rule read-back and write
// paths to the external test package.
var (
	ParsePVERule       = parsePVERule
	BuildPVERuleParams = buildPVERuleParams
)
