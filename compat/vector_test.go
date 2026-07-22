package compat

import (
	"context"
	"reflect"
	"testing"
)

func TestSchemaValidatesVectorDimension(t *testing.T) {
	vector := func(args ...int) Schema {
		return Schema{Tables: []Table{{
			Name: "vecs",
			Columns: []Column{
				{Name: "id", Type: Type{Family: IntegerType}},
				{Name: "v", Type: Type{Family: VectorType, Arguments: args}},
			},
			Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
		}}}
	}
	if err := vector(3).Validate(); err != nil {
		t.Fatalf("vector(3) should validate, got %v", err)
	}
	for _, args := range [][]int{nil, {}, {3, 4}, {0}, {-1}} {
		if err := vector(args...).Validate(); err == nil {
			t.Fatalf("vector%v should be rejected", args)
		}
	}
}

func TestCompileDDLVectorForBothEngines(t *testing.T) {
	schema := Schema{Tables: []Table{{
		Name: "vecs",
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "v", Type: Type{Family: VectorType, Arguments: []int{3}}, Nullable: true},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}

	sqlite, err := CompileDDL(Target{Engine: SQLite, Version: Version{Major: 3}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	wantSQLite := `CREATE TABLE "vecs" ("id" INTEGER NOT NULL, "v" TEXT, PRIMARY KEY ("id"))`
	if sqlite[0] != wantSQLite {
		t.Fatalf("unexpected sqlite DDL: %s", sqlite[0])
	}

	postgres, err := CompileDDL(Target{Engine: Postgres, Version: Version{Major: 17}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	wantPostgres := `CREATE TABLE "vecs" ("id" BIGINT NOT NULL, "v" vector(3), PRIMARY KEY ("id"))`
	if postgres[0] != wantPostgres {
		t.Fatalf("unexpected postgres DDL: %s", postgres[0])
	}
}

func TestCanonicalVectorValue(t *testing.T) {
	canonical, err := canonicalValue(VectorType, "[1, 2.0, 3]", 3)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Kind != VectorValue || canonical.Value != "[1,2,3]" {
		t.Fatalf("unexpected canonical vector %+v", canonical)
	}

	preserved, err := canonicalValue(VectorType, "[1.5, 2.5]", 2)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Value != "[1.5,2.5]" {
		t.Fatalf("expected [1.5,2.5] preserved, got %q", preserved.Value)
	}

	// A dimensionless call (as capture/replication issue) still canonicalizes.
	undimensioned, err := canonicalValue(VectorType, "[1, 2.0, 3]")
	if err != nil {
		t.Fatal(err)
	}
	if undimensioned.Value != "[1,2,3]" {
		t.Fatalf("unexpected undimensioned canonical %q", undimensioned.Value)
	}

	for _, invalid := range []string{"abc", `[1,"x"]`, "not-a-vector", "[1, two, 3]"} {
		if _, err := canonicalValue(VectorType, invalid, 3); err == nil {
			t.Fatalf("expected error for invalid vector %q", invalid)
		}
	}

	if _, err := canonicalValue(VectorType, "[1, 2]", 3); err == nil {
		t.Fatal("expected dimension mismatch error for 2 components with declared dimension 3")
	}
}

func TestSQLiteAndPostgresTypeFamilyForVector(t *testing.T) {
	if family := sqliteTypeFamily("F32_BLOB(3)"); family != VectorType {
		t.Fatalf("F32_BLOB(3) must map to vector, got %q", family)
	}
	if family := sqliteTypeFamily("BLOB"); family != BinaryType {
		t.Fatalf("BLOB must still map to binary, got %q", family)
	}
	if args := parseTypeArguments("F32_BLOB(3)"); !reflect.DeepEqual(args, []int{3}) {
		t.Fatalf("expected dimension [3] from F32_BLOB(3), got %v", args)
	}
	if args := parseTypeArguments("F32_BLOB"); args != nil {
		t.Fatalf("expected nil args without a parenthesized list, got %v", args)
	}

	if family := postgresTypeFamily("boolean"); family != BooleanType {
		t.Fatalf("postgres boolean mapping regressed: %q", family)
	}
	vectorType := postgresType("USER-DEFINED TYPE", "vector", 3)
	if vectorType.Family != VectorType || !reflect.DeepEqual(vectorType.Arguments, []int{3}) {
		t.Fatalf("pgvector vector(3) must map to vector [3], got %+v", vectorType)
	}
	dimensionless := postgresType("USER-DEFINED TYPE", "vector", -1)
	if dimensionless.Family != VectorType || len(dimensionless.Arguments) != 0 {
		t.Fatalf("dimensionless pgvector must be vector with no args, got %+v", dimensionless)
	}
	if family := postgresType("text", "text", -1).Family; family != TextType {
		t.Fatalf("non-vector pg type regressed: %q", family)
	}
}

func TestSQLiteVectorSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	schema := Schema{Tables: []Table{{
		Name: "embeddings",
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "v", Type: Type{Family: VectorType, Arguments: []int{3}}, Nullable: true},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}

	source, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`INSERT INTO embeddings (id, v) VALUES (1, '[1, 2.0, 3]')`); err != nil {
		t.Fatal(err)
	}

	snapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Rows["embeddings"][0]["v"]; got.Kind != VectorValue || got.Value != "[1,2,3]" {
		t.Fatalf("unexpected exported vector %+v", got)
	}

	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	copy, err := destination.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	report, err := VerifySnapshots(snapshot, copy)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equivalent {
		t.Fatalf("snapshots not equivalent: source %s destination %s", report.SourceDigest, report.DestinationDigest)
	}
}

func TestSQLiteVectorSnapshotRejectsDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	schema := Schema{Tables: []Table{{
		Name: "embeddings",
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "v", Type: Type{Family: VectorType, Arguments: []int{3}}, Nullable: true},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	// A 2-component value stored against a declared dimension of 3 must not enter
	// a snapshot silently; ExportSnapshot surfaces the dimension mismatch.
	if _, err := store.DB.Exec(`INSERT INTO embeddings (id, v) VALUES (1, '[1, 2]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExportSnapshot(ctx, schema); err == nil {
		t.Fatal("expected ExportSnapshot to reject a dimension-mismatched vector value")
	}
}

func TestInspectSQLiteF32BlobColumn(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, v F32_BLOB(3))`); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	columns := inspection.Schema.Tables[0].Columns
	var vector Column
	for _, column := range columns {
		if column.Name == "v" {
			vector = column
		}
	}
	if vector.Type.Family != VectorType || !reflect.DeepEqual(vector.Type.Arguments, []int{3}) {
		t.Fatalf("F32_BLOB(3) must inspect as vector [3], got %+v", vector.Type)
	}
	if !inspection.Exact {
		t.Fatalf("inspection should be exact for a plain F32_BLOB table, got unresolved %+v", inspection.Unresolved)
	}
}

func TestInferFeaturesVector(t *testing.T) {
	withVector := Schema{Tables: []Table{{Name: "t", Columns: []Column{
		{Name: "id", Type: Type{Family: IntegerType}},
		{Name: "v", Type: Type{Family: VectorType, Arguments: []int{3}}},
	}}}}
	withoutVector := Schema{Tables: []Table{{Name: "t", Columns: []Column{
		{Name: "id", Type: Type{Family: IntegerType}},
		{Name: "payload", Type: Type{Family: JSONType}},
	}}}}

	if !containsFeature(InferFeatures(withVector), CanonicalVectors) {
		t.Fatal("InferFeatures must include canonical_vectors for a schema with a vector column")
	}
	if containsFeature(InferFeatures(withoutVector), CanonicalVectors) {
		t.Fatal("InferFeatures must not include canonical_vectors without a vector column")
	}
	if finding := assess(CanonicalVectors); finding.Status != Exact {
		t.Fatalf("canonical_vectors must assess as exact, got %s", finding.Status)
	}
}

func containsFeature(features []Feature, want Feature) bool {
	for _, feature := range features {
		if feature == want {
			return true
		}
	}
	return false
}
