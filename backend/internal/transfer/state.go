package transfer

import "fmt"

type State string

const (
	Created            State = "created"
	WaitingForReceiver State = "waiting_for_receiver"
	WaitingForAccept   State = "waiting_for_accept"
	Transferring      State = "transferring"
	Paused             State = "paused"
	Completing         State = "completing"
	Completed          State = "completed"
	Cancelled          State = "cancelled"
	Expired            State = "expired"
	Failed             State = "failed"
)

func CanTransition(from, to State) bool {
	switch from {
	case Created:
		return to == WaitingForReceiver || to == Cancelled || to == Expired
	case WaitingForReceiver:
		return to == WaitingForAccept || to == Cancelled || to == Expired || to == Failed
	case WaitingForAccept:
		return to == Transferring || to == Cancelled || to == Expired || to == Failed
	case Transferring:
		return to == Paused || to == Completing || to == Cancelled || to == Expired || to == Failed
	case Paused:
		return to == Transferring || to == Cancelled || to == Expired || to == Failed
	case Completing:
		return to == Completed || to == Cancelled || to == Expired || to == Failed
	default:
		return false
	}
}

func transition(from *State, to State) error {
	if !CanTransition(*from, to) {
		return fmt.Errorf("invalid transfer transition: %s -> %s", *from, to)
	}
	*from = to
	return nil
}

func IsTerminal(state State) bool {
	return state == Completed || state == Cancelled || state == Expired || state == Failed
}