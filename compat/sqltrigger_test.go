package compat

import (
	"strings"
	"testing"
)

func TestParseSQLiteCatalogTrigger(t *testing.T) {
	trigger, err := parseSQLiteCatalogTrigger(`CREATE TRIGGER capture_product AFTER INSERT ON products FOR EACH ROW BEGIN INSERT INTO audit (code) VALUES (NEW.code); END`)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Name != "capture_product" || trigger.Event != "insert" || len(trigger.Actions) != 1 || trigger.Actions[0].Assignments[0].Value.Value != "NEW.code" {
		t.Fatalf("unexpected trigger: %+v", trigger)
	}
}

func TestParsePostgresCatalogTrigger(t *testing.T) {
	trigger, err := parsePostgresCatalogTrigger(`CREATE TRIGGER capture_product AFTER INSERT ON public.products FOR EACH ROW EXECUTE FUNCTION capture_product_fn()`, `BEGIN INSERT INTO audit (code) VALUES (NEW.code); RETURN NEW; END`)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Table != "products" || len(trigger.Actions) != 1 {
		t.Fatalf("unexpected trigger: %+v", trigger)
	}
}

func TestParseCatalogTriggerUpdateAndDeleteActions(t *testing.T) {
	actions, err := parseCatalogTriggerActions(`UPDATE audit SET code = NEW.code WHERE code = OLD.code; DELETE FROM stale_audit WHERE code = OLD.code;`)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].Kind != "update" || actions[0].Where == nil || actions[1].Kind != "delete" || actions[1].Where == nil {
		t.Fatalf("unexpected actions: %+v", actions)
	}
}

func TestParsePostgresCatalogTriggerAcceptsPublicSchema(t *testing.T) {
	trigger, err := parsePostgresCatalogTrigger(`CREATE TRIGGER capture_product AFTER INSERT ON public.products FOR EACH ROW EXECUTE FUNCTION capture_product_fn()`, `BEGIN INSERT INTO audit (code) VALUES (NEW.code); RETURN NEW; END`)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Table != "products" {
		t.Fatalf("expected Table %q, got %q", "products", trigger.Table)
	}
}

func TestParsePostgresCatalogTriggerRejectsNonPublicSchema(t *testing.T) {
	_, err := parsePostgresCatalogTrigger(`CREATE TRIGGER capture_product AFTER INSERT ON myschema.products FOR EACH ROW EXECUTE FUNCTION capture_product_fn()`, `BEGIN INSERT INTO audit (code) VALUES (NEW.code); RETURN NEW; END`)
	if err == nil {
		t.Fatalf("expected error for non-public schema, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), "myschema") {
		t.Fatalf("expected unsupported error naming schema, got %q", err.Error())
	}
}

// TestParsePostgresCatalogTriggerSchemaEquivalence covers the case-sensitive
// semantics of quoted PostgreSQL identifiers. A quoted qualifier is a distinct
// schema unless it is exactly lowercase "public"; an unquoted qualifier is
// folded to lowercase by PostgreSQL, so any case variant matches public.
func TestParsePostgresCatalogTriggerSchemaEquivalence(t *testing.T) {
	body := `BEGIN INSERT INTO audit (code) VALUES (NEW.code); RETURN NEW; END`
	cases := []struct {
		name       string
		definition string
		wantErr    bool
		wantTable  string
		errSubstr  string
	}{
		{
			name:       `quoted "Public" rejected`,
			definition: `CREATE TRIGGER capture_product AFTER INSERT ON "Public".products FOR EACH ROW EXECUTE FUNCTION capture_product_fn()`,
			wantErr:    true,
			errSubstr:  "Public",
		},
		{
			name:       `quoted "public" accepted`,
			definition: `CREATE TRIGGER capture_product AFTER INSERT ON "public".products FOR EACH ROW EXECUTE FUNCTION capture_product_fn()`,
			wantTable:  "products",
		},
		{
			name:       `unquoted PUBLIC accepted`,
			definition: `CREATE TRIGGER capture_product AFTER INSERT ON PUBLIC.products FOR EACH ROW EXECUTE FUNCTION capture_product_fn()`,
			wantTable:  "products",
		},
		{
			name:       `unquoted otherschema rejected`,
			definition: `CREATE TRIGGER capture_product AFTER INSERT ON otherschema.products FOR EACH ROW EXECUTE FUNCTION capture_product_fn()`,
			wantErr:    true,
			errSubstr:  "otherschema",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trigger, err := parsePostgresCatalogTrigger(tc.definition, body)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got trigger %+v", trigger)
				}
				if !strings.Contains(err.Error(), "unsupported") {
					t.Fatalf("expected unsupported error, got %q", err.Error())
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("expected error to name schema %q, got %q", tc.errSubstr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if trigger.Table != tc.wantTable {
				t.Fatalf("expected Table %q, got %q", tc.wantTable, trigger.Table)
			}
		})
	}
}
