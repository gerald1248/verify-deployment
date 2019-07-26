package main

func getState(name, namespace string, replicas int, time int64) State {
	var state State
	state.name = name
	state.namespace = namespace
	state.replicas = replicas
	state.running = 0
	state.time = time

	return state
}
