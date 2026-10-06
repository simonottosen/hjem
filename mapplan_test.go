package hjem

import "testing"

func mapAddr(street, number, zip string, lat, lon float64) *Address {
	return &Address{
		StreetName:       street,
		StreetNumber:     number,
		PostalCode:       zip,
		MunicipalityCode: "101",
		// Crossed over, because Address stores x in .Latitude and y in
		// .Longtitude. Writing them straight through would make the test agree
		// with a buildMapPlan that reads the fields at face value — which is
		// exactly the bug the crossing exists to catch.
		Latitude:   lon,
		Longtitude: lat,
	}
}

func (p *MapPlan) street(t *testing.T, name string) MapStreet {
	t.Helper()
	for _, s := range p.Streets {
		if s.Task.StreetName == name {
			return s
		}
	}
	t.Fatalf("no street %q in plan (%d streets)", name, len(p.Streets))
	return MapStreet{}
}

// A block of flats shares one access point, so plotting every unit would stack
// dozens of dots on one coordinate — the dense-area case the map has to stay
// smooth in.
func TestMapPlanPlotsEachBuildingOnce(t *testing.T) {
	primary := mapAddr("Flensborggade", "40", "1669", 55.6700, 12.5500)
	addrs := []*Address{
		primary,
		mapAddr("Istedgade", "10", "1650", 55.6710, 12.5510),
		mapAddr("Istedgade", "10", "1650", 55.6710, 12.5510),
		mapAddr("Istedgade", "10", "1650", 55.67100004, 12.55100004),
		mapAddr("Istedgade", "12", "1650", 55.6711, 12.5512),
	}

	plan := buildMapPlan(primary, addrs, []int{250, 500})

	if plan.RadiusM != 500 {
		t.Errorf("radius = %d, want the widest requested range 500", plan.RadiusM)
	}
	if got := plan.street(t, "Istedgade").Points; len(got) != 2 {
		t.Errorf("Istedgade points = %v, want one per building", got)
	}
}

// The searched property is drawn as its own marker. If it also appeared in a
// street's points, a neighbouring flat at the same staircase would bury it.
func TestMapPlanExcludesTheSearchedProperty(t *testing.T) {
	primary := mapAddr("Flensborggade", "40", "1669", 55.6700, 12.5500)
	addrs := []*Address{
		primary,
		mapAddr("Flensborggade", "40", "1669", 55.6700, 12.5500),
		mapAddr("Flensborggade", "42", "1669", 55.6701, 12.5501),
	}

	plan := buildMapPlan(primary, addrs, []int{250})

	if plan.Lat != 55.6700 || plan.Lon != 12.5500 {
		t.Errorf("centre = %v,%v, want the searched property", plan.Lat, plan.Lon)
	}
	if got := plan.street(t, "Flensborggade").Points; len(got) != 1 {
		t.Errorf("Flensborggade points = %v, want only the neighbour", got)
	}
}

// Streets are the reveal boundary the client animates on, so every one of them
// has to be keyed exactly as a fetch task is. A street the task list cannot
// produce is one the client reads as already cached and reveals at once — it
// would light up buildings nobody has fetched yet.
func TestMapPlanStreetsAreKeyedAsFetchTasks(t *testing.T) {
	primary := mapAddr("Flensborggade", "40", "1669", 55.6700, 12.5500)
	addrs := []*Address{
		primary,
		mapAddr("Istedgade", "10", "1650", 55.6710, 12.5510),
		mapAddr("Istedgade", "12", "1650", 55.6711, 12.5512),
		mapAddr("Sønder Boulevard", "1", "1720", 55.6720, 12.5520),
	}

	plan := buildMapPlan(primary, addrs, []int{250})

	fetchable := map[BoligaPropertyRequest]bool{}
	for _, task := range BoligaStreetTasks(addrs) {
		fetchable[task] = true
	}
	for _, s := range plan.Streets {
		if !fetchable[s.Task] {
			t.Errorf("map street %+v matches no fetch task", s.Task)
		}
	}
	if len(plan.Streets) != 2 {
		t.Errorf("got %d streets, want Istedgade and Sønder Boulevard", len(plan.Streets))
	}
}

// Datafordeleren occasionally returns an address without a position. Plotting
// it would drop a dot in the Gulf of Guinea, a long way outside the radius the
// map is framed to.
func TestMapPlanSkipsAddressesWithoutCoordinates(t *testing.T) {
	primary := mapAddr("Flensborggade", "40", "1669", 55.6700, 12.5500)
	addrs := []*Address{
		primary,
		mapAddr("Istedgade", "10", "1650", 0, 0),
	}

	plan := buildMapPlan(primary, addrs, []int{250})

	for _, s := range plan.Streets {
		if len(s.Points) > 0 {
			t.Errorf("street %q plotted %v, want nothing", s.Task.StreetName, s.Points)
		}
	}
}
