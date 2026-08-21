package storage

import _ "embed"

const SchemaVersion = 1

//go:embed schema-v1.sql
var schemaV1 string
