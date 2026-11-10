package doc

import _ "embed"

//go:embed ENVIRONMENT-REGISTRY-v3.csv
var registryCSV string

func RegistryCSV() string { return registryCSV }
