package models

// CouncillorType represents the type of council position
type CouncillorType string

const (
	CouncillorTypeMayor   CouncillorType = "mayor"
	CouncillorTypeAtLarge CouncillorType = "atlarge"
	CouncillorTypeWard    CouncillorType = "ward"
)

// Councillor represents a Thunder Bay city councillor
type Councillor struct {
	Name         string
	Position     string // Role or ward name (e.g., "Budget Chair", "Current River")
	Term         string // e.g., "5th term"
	Status       string // e.g., "Not seeking re-election"
	Summary      string
	ShortSummary string // one-liner for map popups
	Photo        string // filename in static/councillors/ (e.g., "boshcoff.jpg")
	Type         CouncillorType
}

// KeyVote represents a significant council vote
type KeyVote struct {
	Issue    string
	Result   string
	Vote     string // e.g., "7-6", "-" if not applicable
	MediaURL string // link to press coverage
}

// CouncilStats holds compensation and structure information
type CouncilStats struct {
	MayorSalary        string
	CouncillorSalary   string
	TotalAnnual        string
	SalaryIncreaseNote string
	TermLength         string
	CurrentTerm        string
	NextElection       string
	Source             SourceRef
}
