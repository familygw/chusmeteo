package locate

import "testing"

func TestParse(t *testing.T) {
	loc, err := parse([]byte(`{"latitude":"-31.420082","longitude":"-64.188776","locality":"Córdoba","administrativeArea":"Córdoba","country":"Argentina","address":null}`))
	if err != nil || loc.Name != "Córdoba" || loc.Latitude > -31.42 || loc.Latitude < -31.43 || loc.Longitude > -64.18 {
		t.Fatalf("loc=%+v err=%v", loc, err)
	}
	loc, err = parse([]byte(`{"latitude":-31.4,"longitude":-64.2}`)) // números sin comillas y sin ciudad
	if err != nil || loc.Name != "Mi ubicación" {
		t.Fatalf("loc=%+v err=%v", loc, err)
	}
	if _, err := parse([]byte(`{}`)); err == nil {
		t.Error("sin coordenadas debe fallar")
	}
}
