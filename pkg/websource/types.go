package websource

type SearchHit struct {
	Title    string
	URL      string
	Snippet  string
	Provider string
}

type Document struct {
	URL       string
	Status    int
	Extractor string
	Truncated bool
	Length    int
	Title     string
	LeadText  string
	Text      string
}
