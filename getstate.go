package main

func getState(name, namespace string, replicas int, time int64, debug bool) State {
	var state State
	state.name = name
	state.namespace = namespace
	state.replicas = replicas
	state.running = 0
	state.time = time
	state.debug = debug

	return state
}
