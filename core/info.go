package core

var Infostats [4]map[string]int

func UpdateStats(db int, metric string, value int) {
	Infostats[db][metric] = value
}
