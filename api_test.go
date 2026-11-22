package txtab

import (
	"strings"
	"testing"
)

const orders = `# texttables=0.1 delim=comma
# meta-source shop_db.orders
# table name=orders rows=3
# column name=id type=long role=id
# column name=amount type=decimal unit=USD role=measure agg=sum
# agg-sum ,1050.50
# sql-pre-data postgres CREATE TABLE orders (id bigint, amount numeric)
# sql-pre-data mysql-8.4 CREATE TABLE orders (id bigint, amount decimal(12,2))
# note key=1003 col=amount text="Refund pending"
id,amount
1001,500.00
1002,300.50
1003,250.00
`

func TestParseOrders(t *testing.T) {
	doc := Parse([]byte(orders))
	if !doc.OK || len(doc.Warnings()) != 0 {
		t.Fatalf("ok=%v error=%v warnings=%v", doc.OK, doc.Error, doc.Warnings())
	}
	tb := doc.Table("orders")
	if tb.Rows != 3 || tb.RowsRead != 3 || js(tb.Data.Header) != `["id","amount"]` {
		t.Errorf("table: %+v", tb)
	}
	if js(tb.Aggregates["sum"]) != `["","1050.50"]` {
		t.Errorf("agg-sum: %v", tb.Aggregates["sum"])
	}
	if n := tb.Notes[0]; n.ResolvedRow != 2 || n.Target != "cell" {
		t.Errorf("note: %+v", n)
	}
}

func TestApplySQL(t *testing.T) {
	doc := Parse([]byte(orders))
	a := ApplySQL(doc, "mysql", false, "", nil)
	if lines := a.Lines(); len(lines) != 1 || lines[0].Token != "mysql-8.4" || len(a.Warnings) != 1 || a.Warnings[0].Code != "sql-dialect-family" {
		t.Errorf("lines=%v warnings=%v", a.Lines(), a.Warnings)
	}
}

func TestFailStops(t *testing.T) {
	doc := Parse([]byte("# texttables=0.1\n# table\nid\n1\n"))
	if doc.OK || doc.Error.Code != "table-missing-rows" || doc.Error.Line != 2 {
		t.Errorf("%+v", doc.Error)
	}
}

func TestNotTxtab(t *testing.T) {
	if Parse([]byte("id,name\n1,Ann\n")).Recognized {
		t.Error("plain CSV recognized")
	}
}

func TestDescribeRoundTrip(t *testing.T) {
	text, err := DescribeCSV("Order ID;Amount;Note\n1;10.50;\"a; b\"\n2;3;\n", "orders.csv")
	if err != nil {
		t.Fatal(err)
	}
	doc := Parse([]byte(text))
	if !doc.OK || len(doc.Warnings()) != 0 {
		t.Fatalf("%s\nerror=%v warnings=%v", text, doc.Error, doc.Warnings())
	}
	var types []string
	for _, c := range doc.Tables[0].Columns {
		types = append(types, c.Type)
	}
	if strings.Join(types, ",") != "int,decimal,string" || js(doc.Tables[0].Aggregates["sum"][:2]) != `["3","13.50"]` {
		t.Errorf("types %v sum %v", types, doc.Tables[0].Aggregates["sum"])
	}
}

func TestFormula(t *testing.T) {
	refs, err := ParseFormula("case when qty > 0 then coalesce(price, 0) * qty else null end")
	if err != nil || js(refs) != `["qty","price"]` {
		t.Errorf("refs=%v err=%v", refs, err)
	}
	if _, err := ParseFormula("a +* 2"); err == nil {
		t.Error("a +* 2 parsed")
	}
}
