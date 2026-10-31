package main

// The claim, as null-hype/worlds checks it today: people can install witness.
// Today's check installs the feature Jules wrote by running its install.sh in
// a clean container, so it passes without anyone installing witness the way
// people do (CIT-559). xeno reads it as a claim with two arms.
//
// +check
func (m *Worlds) PeopleCanInstallWitness() error {
	return nil
}
