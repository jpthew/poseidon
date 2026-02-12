//go:build ios

package listtasks

type DarwinListTasks struct {}
func (d DarwinListTasks) Result() map[string]interface{} { return map[string]interface{}{} }

func getAvailableTasks() (Listtasks, error) {
	return DarwinListTasks{}, nil
}
