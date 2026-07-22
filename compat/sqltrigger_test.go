package compat

import "testing"

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
