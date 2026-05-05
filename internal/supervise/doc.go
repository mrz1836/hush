// Package supervise owns the supervisor daemon's lifecycle state
// machine, child runner, refill/refresh/grace logic, status socket,
// and watchdog. SDD-19 ships the lifecycle state machine and
// snapshot store; subsequent chunks (SDD-20..22, SDD-25) add
// behaviour on top of this package without modifying the locked
// API below.
package supervise
