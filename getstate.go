package main

func getState(name, namespace string, time int64) State {
	var state State
	state.name = name
	state.namespace = namespace
	state.running = 0
	state.time = time

	return state
}
