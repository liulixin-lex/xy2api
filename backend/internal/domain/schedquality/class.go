package schedquality

// ErrorClass values are persisted; do not renumber them.
type ErrorClass int8

const (
	ClassNone               ErrorClass = 0
	ClassUpstream           ErrorClass = 1
	ClassInterrupted        ErrorClass = 2
	ClassAccountAccess      ErrorClass = 3
	ClassAccountCapacity    ErrorClass = 4
	ClassRequest            ErrorClass = 5
	ClassMigration400       ErrorClass = 6
	ClassCapability         ErrorClass = 7
	ClassFirstOutputTimeout ErrorClass = 8
	ClassGateway            ErrorClass = 9
	ClassClientCancel       ErrorClass = 10
	ClassRepairStep         ErrorClass = 11
	ClassUnknown            ErrorClass = 12
)

func (c ErrorClass) Counted() bool { return c >= ClassUpstream && c <= ClassAccountCapacity }

func (c ErrorClass) String() string {
	switch c {
	case ClassNone:
		return "none"
	case ClassUpstream:
		return "upstream"
	case ClassInterrupted:
		return "interrupted"
	case ClassAccountAccess:
		return "account_access"
	case ClassAccountCapacity:
		return "account_capacity"
	case ClassRequest:
		return "request"
	case ClassMigration400:
		return "migration_400"
	case ClassCapability:
		return "capability"
	case ClassFirstOutputTimeout:
		return "first_output_timeout"
	case ClassGateway:
		return "gateway"
	case ClassClientCancel:
		return "client_cancel"
	case ClassRepairStep:
		return "repair_step"
	case ClassUnknown:
		return "unknown"
	}
	return "unknown"
}

type StatusOrigin int8

const (
	OriginNone     StatusOrigin = 0
	OriginUpstream StatusOrigin = 1
	OriginGateway  StatusOrigin = 2
)

func (o StatusOrigin) String() string {
	switch o {
	case OriginNone:
		return "none"
	case OriginUpstream:
		return "upstream"
	case OriginGateway:
		return "gateway"
	}
	return "unknown"
}
