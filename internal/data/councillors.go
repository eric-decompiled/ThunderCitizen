package data

import (
	"fmt"
	"sort"
	"strings"

	"thundercitizen/internal/models"
)

// CouncilTerm holds all council data for a single election cycle.
type CouncilTerm struct {
	Mayor    models.Councillor
	AtLarge  []models.Councillor
	Ward     []models.Councillor
	KeyVotes []models.KeyVote
	Stats    models.CouncilStats
}

// DefaultTerm is the election year shown by default.
const DefaultTerm = 2022

// TermRange returns the hyphenated term key for a given election year (e.g. "2022-2026").
func TermRange(year int) string {
	return fmt.Sprintf("%d-%d", year, year+4)
}

// ElectionLabel returns the display label for an election year (e.g. "2022–2026").
func ElectionLabel(year int) string {
	return electionLabels[year]
}

// ElectionLabels returns the label map for all available terms.
func ElectionLabels() map[int]string {
	labels := make(map[int]string, len(electionLabels))
	for k, v := range electionLabels {
		labels[k] = v
	}
	return labels
}

// AvailableTerms returns election years in descending order.
func AvailableTerms() []int {
	years := make([]int, 0, len(CouncilByTerm))
	for y := range CouncilByTerm {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	return years
}

// FindCouncillorBySlug looks up a councillor by URL slug (lowercased last name, no punctuation).
// Returns the councillor, the election year, and true if found.
func FindCouncillorBySlug(slug string) (models.Councillor, int, bool) {
	for _, year := range AvailableTerms() {
		term := CouncilByTerm[year]
		if slugMatch(term.Mayor.Name, slug) {
			return term.Mayor, year, true
		}
		for _, c := range term.AtLarge {
			if slugMatch(c.Name, slug) {
				return c, year, true
			}
		}
		for _, c := range term.Ward {
			if slugMatch(c.Name, slug) {
				return c, year, true
			}
		}
	}
	return models.Councillor{}, 0, false
}

func slugMatch(name, slug string) bool {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return false
	}
	last := strings.ToLower(parts[len(parts)-1])
	// Remove non-alpha characters
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r
		}
		return -1
	}, last)
	return clean == slug
}

var electionLabels = map[int]string{
	2022: "2022–2026",
	2018: "2018–2022",
	2014: "2014–2018",
	2010: "2010–2014",
}

// Backward-compatible accessors for current term.
var (
	Mayor              = CouncilByTerm[DefaultTerm].Mayor
	AtLargeCouncillors = CouncilByTerm[DefaultTerm].AtLarge
	WardCouncillors    = CouncilByTerm[DefaultTerm].Ward
	KeyVotes           = CouncilByTerm[DefaultTerm].KeyVotes
	CouncilStats       = CouncilByTerm[DefaultTerm].Stats
)

// CouncilByTerm maps election year -> council data.
var CouncilByTerm = map[int]CouncilTerm{
	2022: {
		Mayor: models.Councillor{
			Name:         "Ken Boshcoff",
			Position:     "Mayor",
			Term:         "5th term",
			Status:       "Not seeking re-election",
			Summary:      "Lakehead University economics graduate with a Master's in Environmental Studies from York University. Served as councillor (1979), mayor (1997–2003), and federal MP for Thunder Bay–Rainy River (2004–2008) before returning as mayor in 2022. Instrumental in establishing the Northern Ontario School of Medicine and is the only mayor to have served as president of all three major Ontario municipal associations.",
			ShortSummary: "Former mayor, MP, and longest-serving municipal leader in Thunder Bay history.",
			Photo:        "boshcoff.jpg",
			Type:         models.CouncillorTypeMayor,
		},
		AtLarge: []models.Councillor{
			{
				Name:         "Trevor Giertuga",
				Term:         "6th term",
				Summary:      "Third-generation Thunder Bay resident first elected in 2000, making him the longest continuously serving councillor. Works as Deputy Superintendent of Programs at the Thunder Bay Correctional Centre and previously owned a small business in McIntyre Ward. Known for constituent accessibility and focus on economic development, youth employment, and public safety.",
				ShortSummary: "Longest-serving councillor, first elected in 2000.",
				Photo:        "giertuga.jpg",
				Type:         models.CouncillorTypeAtLarge,
			},
			{
				Name:         "Mark Bentz",
				Position:     "Budget Chair",
				Term:         "5th term",
				Summary:      "Holds degrees in Physics and Education; currently a professor at Confederation College's School of Aviation, Engineering Technology and Trades. Chaired the Waterfront Development Committee that helped attract over $100 million in investment to Prince Arthur's Landing. Chairs Administrative Services overseeing the city's annual budget process.",
				ShortSummary: "Confederation College professor and Budget Chair.",
				Photo:        "bentz.jpg",
				Type:         models.CouncillorTypeAtLarge,
			},
			{
				Name:         "Shelby Ch'ng",
				Term:         "3rd term",
				Summary:      "Honours Political Science graduate from Lakehead University who owned Unveiled Bridal in Thunder Bay's South Core for nearly a decade. Won the 2019 Wheels of Change Community Champion of the Year award for active transportation advocacy. Carries one of the largest committee portfolios on council, including Vice-Chair of Intergovernmental Affairs.",
				ShortSummary: "Active transportation advocate, Vice-Chair of Intergovernmental Affairs.",
				Photo:        "chng.jpg",
				Type:         models.CouncillorTypeAtLarge,
			},
			{
				Name:         "Kasey Etreni",
				Term:         "1st term",
				Summary:      "Retired Charge Radiation Therapist from Thunder Bay Regional Health Sciences Centre with a B.Sc. (Honours) from Western Michigan University. Served on the Board of Directors of the Ontario Association of Medical Radiation Sciences. Sits on boards for the Thunder Bay Police Service, Age Friendly Thunder Bay, and the District Health Unit.",
				ShortSummary: "Retired radiation therapist, serves on Police and Health boards.",
				Photo:        "etreni.jpg",
				Type:         models.CouncillorTypeAtLarge,
			},
			{
				Name:         "Rajni Agarwal",
				Term:         "1st term",
				Summary:      "University of Toronto science graduate and licensed Ontario real estate broker since 1991. Developer behind Terra Vista Condominiums, Thunder Bay's largest condominium development. Business experience spans real estate, land development, property management, and international education.",
				ShortSummary: "Real estate developer behind Terra Vista Condominiums.",
				Photo:        "agarwal.jpg",
				Type:         models.CouncillorTypeAtLarge,
			},
		},
		Ward: []models.Councillor{
			{
				Name:         "Andrew Foulds",
				Position:     "Current River",
				Term:         "5th term",
				Summary:      "Honours B.Sc. from University of Toronto and B.Ed. from Queen's University; teaches Biology at Westgate Collegiate. Championed the Boulevard Lake dam rehabilitation for over 13 years, helping secure $13.2 million in federal funding.",
				ShortSummary: "Biology teacher, championed Boulevard Lake dam rehabilitation.",
				Photo:        "foulds.jpg",
				Type:         models.CouncillorTypeWard,
			},
			{
				Name:         "Albert Aiello",
				Position:     "McIntyre",
				Term:         "2nd term",
				Summary:      "Spent 29 years with the Boys and Girls Club of Thunder Bay, serving as Executive Director since 2001. Created the city's first Community Hub and is a graduate of the inaugural Leadership Thunder Bay program. Advocates for practical infrastructure improvements including designated truck routes.",
				ShortSummary: "Former Boys and Girls Club Executive Director.",
				Photo:        "aiello.jpg",
				Type:         models.CouncillorTypeWard,
			},
			{
				Name:         "Brian Hamilton",
				Position:     "McKellar",
				Term:         "2nd term",
				Summary:      "Long-time small business owner in Thunder Bay's downtown core with over 20 years of experience in economic development. Past president of the Bay and Algoma Business District who helped establish Thunder Bay's first Buskers Festival. Serves on the District Social Services Administration Board advocating for the region's most vulnerable citizens.",
				ShortSummary: "Downtown small business owner, DSSAB board member.",
				Photo:        "hamilton.jpg",
				Type:         models.CouncillorTypeWard,
			},
			{
				Name:         "Kristen Oliver",
				Position:     "Westfort",
				Term:         "2nd term",
				Summary:      "Confederation College graduate serving as Senior Advisor for Municipal and Stakeholder Engagement at Enbridge Gas Ontario. Previously spent seven years as Executive Director of the Northwestern Ontario Municipal Association, lobbying provincial and federal governments. Chairs the Intergovernmental Affairs Committee and serves as a Board of Governor at Confederation College.",
				ShortSummary: "Enbridge advisor, former NOMA Executive Director.",
				Photo:        "oliver.jpg",
				Type:         models.CouncillorTypeWard,
			},
			{
				Name:         "Greg Johnsen",
				Position:     "Neebing",
				Term:         "1st term",
				Summary:      "Holds a Master of Arts in History and teaches law, philosophy, and history at St. Patrick High School. Serves on the Board of Directors of the Thunder Bay Museum. Championed a bylaw requiring all councillors to host annual public ward meetings, taking effect in 2027.",
				ShortSummary: "History teacher, championed public ward meetings bylaw.",
				Photo:        "johnsen.jpg",
				Type:         models.CouncillorTypeWard,
			},
			{
				Name:         "Dominic Pasqualino",
				Position:     "Northwood",
				Term:         "1st term",
				Summary:      "Spent 35 years at the Can Car/Bombardier/Alstom rail plant and served 11 years as President of Unifor Local 1075. Met with over 108 federal representatives to lobby for transit manufacturing contracts that preserved local jobs. Advocates on council for infrastructure improvements and community services in Northwood.",
				ShortSummary: "35-year rail worker, former Unifor Local 1075 president.",
				Photo:        "pasqualino.jpg",
				Type:         models.CouncillorTypeWard,
			},
			{
				Name:         "Michael Zussino",
				Position:     "Red River",
				Term:         "1st term",
				Summary:      "Lifelong Red River resident with a B.Sc. and B.Ed. from Lakehead University. 25-year educator with the Thunder Bay Catholic District School Board. Advocates for community health and recreation infrastructure, including supporting the city's $42-million indoor turf sports facility.",
				ShortSummary: "25-year educator, recreation infrastructure advocate.",
				Photo:        "zussino.jpg",
				Type:         models.CouncillorTypeWard,
			},
		},
		KeyVotes: []models.KeyVote{
			{Issue: "Police Budget (9.1% increase)", Result: "Passed", Vote: "7-6", MediaURL: "https://www.tbnewswatch.com/local-news/police-service-proposes-91-per-cent-budget-increase-11646061"},
			{Issue: "Shelter Village Site (Miles St)", Result: "Reversed", Vote: "-", MediaURL: "https://www.tbnewswatch.com/local-news/miles-street-moved-forward-as-a-temporary-village-site-10943699"},
			{Issue: "Advisory Committees Dissolution", Result: "Passed", Vote: "9-4", MediaURL: "https://www.tbnewswatch.com/local-news/council-passes-governance-changes-10857571"},
			{Issue: "Strong Mayor Powers", Result: "Council opposed", Vote: "9-4", MediaURL: "https://www.tbnewswatch.com/local-news/council-rejects-boshcoffs-ask-for-strong-mayor-support-7566107"},
		},
		Stats: models.CouncilStats{
			MayorSalary:        "$95,000 -> $100,941",
			CouncillorSalary:   "$31,000 -> $33,183",
			TotalAnnual:        "$467,000 → $500,000",
			SalaryIncreaseNote: "~1.8%/yr avg",
			TermLength:         "4 years",
			CurrentTerm:        "2022–2026",
			NextElection:       "October 2026",
			Source:             models.SourceRef{URL: "https://www.thunderbay.ca/en/city-hall/mayor-and-council-profiles.aspx"},
		},
	},
	2018: {
		Mayor: models.Councillor{
			Name:         "Bill Mauro",
			Position:     "Mayor",
			Term:         "1st term",
			Summary:      "Former Liberal MPP for Thunder Bay–Atikokan (2003–2018) who held multiple cabinet portfolios including Municipal Affairs, Revenue, and Natural Resources. Won the 2018 mayoral race after leaving provincial politics. Previously served as a school trustee with the Thunder Bay Catholic District School Board.",
			ShortSummary: "Former provincial MPP with multiple cabinet portfolios.",
			Photo:        "mauro.jpg",
			Type:         models.CouncillorTypeMayor,
		},
		AtLarge: []models.Councillor{
			{Name: "Trevor Giertuga", Term: "5th term", Summary: "Third-generation Thunder Bay resident first elected in 2000. Works as Deputy Superintendent of Programs at the Thunder Bay Correctional Centre and previously owned a small business in McIntyre Ward. Known for constituent accessibility and focus on economic development and public safety.", ShortSummary: "Longest-serving councillor, first elected in 2000.", Photo: "giertuga.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Mark Bentz", Term: "4th term", Summary: "Holds degrees in Physics and Education; professor at Confederation College's School of Aviation, Engineering Technology and Trades. Chaired the Waterfront Development Committee that helped attract over $100 million in investment to Prince Arthur's Landing.", ShortSummary: "Confederation College professor, waterfront committee chair.", Photo: "bentz.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Rebecca Johnson", Term: "2nd term", Summary: "Business owner of Rebecca Reports, a governance consulting firm. After her husband's death at age 25, managed the family farm while raising four children before entering public service. Hosted 'Community Clipboard' on Shaw Cable for 20 years and served on more than 20 boards including Lakehead University and Diversity Thunder Bay. Recipient of the Queen Elizabeth Diamond Jubilee Medal.", ShortSummary: "Governance consultant and community media host.", Photo: "johnson.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Peng You", Term: "2nd term", Summary: "Moved to Thunder Bay from China in 1990. Internationally recognized Tai Chi Master who founded the Peng You International Tai Chi Academy. Spearheaded the International Tai Chi Park on the waterfront and facilitated the donation of Tai Chi statues from sister city Jiaozuo. Helped secure hosting of the 2022 National Wushu Championship. Winner of the Thunder Bay Chamber Business Excellence and Ambassador Awards.", ShortSummary: "Tai Chi master, international cultural ambassador.", Photo: "you.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Aldo Ruberto", Term: "8th term", Summary: "First elected in 1988, making him the longest-serving member in Thunder Bay City Council history. A dedicated community advocate who has represented residents across eight consecutive terms spanning more than three decades of municipal governance.", ShortSummary: "Longest-serving councillor in city history.", Photo: "ruberto.jpg", Type: models.CouncillorTypeAtLarge},
		},
		Ward: []models.Councillor{
			{Name: "Andrew Foulds", Position: "Current River", Term: "4th term", Summary: "Honours B.Sc. from University of Toronto and B.Ed. from Queen's University; teaches Biology at Westgate Collegiate. Championed the Boulevard Lake dam rehabilitation for over a decade, helping secure millions in federal funding.", ShortSummary: "Biology teacher, Boulevard Lake dam champion.", Photo: "foulds.jpg", Type: models.CouncillorTypeWard},
			{Name: "Albert Aiello", Position: "McIntyre", Term: "1st term", Summary: "Spent 29 years with the Boys and Girls Club of Thunder Bay, serving as Executive Director since 2001. Created the city's first Community Hub and is a graduate of the inaugural Leadership Thunder Bay program.", ShortSummary: "Boys and Girls Club Executive Director.", Photo: "aiello.jpg", Type: models.CouncillorTypeWard},
			{Name: "Brian Hamilton", Position: "McKellar", Term: "1st term", Summary: "Long-time small business owner in Thunder Bay's downtown core with over 20 years of experience in economic development. Past president of the Bay and Algoma Business District who helped establish Thunder Bay's first Buskers Festival.", ShortSummary: "Downtown small business owner.", Photo: "hamilton.jpg", Type: models.CouncillorTypeWard},
			{Name: "Kristen Oliver", Position: "Westfort", Term: "1st term", Summary: "Confederation College graduate serving as Senior Advisor for Municipal and Stakeholder Engagement at Enbridge Gas Ontario. Previously spent seven years as Executive Director of the Northwestern Ontario Municipal Association, lobbying provincial and federal governments.", ShortSummary: "Former NOMA Executive Director.", Photo: "oliver.jpg", Type: models.CouncillorTypeWard},
			{Name: "Cody Fraser", Position: "Neebing", Term: "1st term", Summary: "Lawyer practising at Cheadles LLP in Thunder Bay. Defeated long-serving incumbent Linda Rydholm to win the Neebing ward seat in 2018. Did not seek re-election in 2022.", ShortSummary: "Lawyer and first-term Neebing ward representative.", Type: models.CouncillorTypeWard},
			{Name: "Shelby Ch'ng", Position: "Northwood", Term: "2nd term", Summary: "Honours Political Science graduate from Lakehead University who owned Unveiled Bridal in Thunder Bay's South Core. Won the 2019 Wheels of Change Community Champion of the Year award for active transportation advocacy. Previously served as at-large councillor in the 2014–2018 term.", ShortSummary: "Active transportation advocate and small business owner.", Photo: "chng.jpg", Type: models.CouncillorTypeWard},
			{Name: "Brian McKinnon", Position: "Red River", Term: "2nd term", Summary: "Retired educator with a long career in the Thunder Bay school system. Community safety advocate who previously served as at-large councillor in the 2014–2018 term before representing Red River ward.", ShortSummary: "Retired educator focused on community safety.", Photo: "mckinnon.jpg", Type: models.CouncillorTypeWard},
		},
		KeyVotes: []models.KeyVote{
			{Issue: "Event Centre (downtown arena)", Result: "Passed", Vote: "8-5", MediaURL: "https://www.tbnewswatch.com/local-news/a-live-update-of-tonights-special-event-centre-session-of-city-council-400679"},
			{Issue: "COVID-19 Emergency Declaration", Result: "Declared", Vote: "-", MediaURL: "https://www.tbnewswatch.com/local-news/mayor-declares-state-of-emergency-2277427"},
			{Issue: "Supervised Consumption Site", Result: "Council supported", Vote: "8-5"},
		},
		Stats: models.CouncilStats{
			MayorSalary:        "$89,000 -> $95,000",
			CouncillorSalary:   "$29,000 -> $31,000",
			TotalAnnual:        "$437,000 → $467,000",
			SalaryIncreaseNote: "~1.7%/yr avg",
			TermLength:         "4 years",
			CurrentTerm:        "2018–2022",
			NextElection:       "October 2022",
			Source:             models.SourceRef{URL: "https://www.cbc.ca/news/canada/thunder-bay/thunder-bay-city-council-increase-1.4629150"},
		},
	},
	2014: {
		Mayor: models.Councillor{
			Name:         "Keith Hobbs",
			Position:     "Mayor",
			Term:         "2nd term",
			Summary:      "Former Thunder Bay Police Service officer who served 25 years before entering politics. First elected mayor in 2010. During his second term, faced extortion charges in 2017 related to a housing dispute; acquitted in 2019 after trial.",
			ShortSummary: "Former police officer serving second term as mayor.",
			Photo:        "hobbs.jpg",
			Type:         models.CouncillorTypeMayor,
		},
		AtLarge: []models.Councillor{
			{Name: "Trevor Giertuga", Term: "4th term", Summary: "Deputy Superintendent of Programs at the Thunder Bay Correctional Centre. First elected in 2000, bringing extensive experience in community safety and economic development to council.", ShortSummary: "Longest-serving councillor, correctional services.", Photo: "giertuga.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Mark Bentz", Term: "3rd term", Summary: "Professor at Confederation College and champion of the Prince Arthur's Landing waterfront development, helping attract over $100 million in investment to the area.", ShortSummary: "Confederation College professor, waterfront champion.", Photo: "bentz.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Shelby Ch'ng", Term: "1st term", Summary: "Honours Political Science graduate from Lakehead University who owned Unveiled Bridal in Thunder Bay's South Core. Won her first council seat in the 2014 election and quickly became an advocate for active transportation.", ShortSummary: "Small business owner and political science graduate.", Photo: "chng.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Brian McKinnon", Term: "1st term", Summary: "Retired educator with a long career in the Thunder Bay school system. First elected in 2014 with a focus on community safety and neighbourhood engagement.", ShortSummary: "Retired educator, community safety advocate.", Photo: "mckinnon.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Aldo Ruberto", Term: "7th term", Summary: "First elected in 1988, the longest continuously serving member of Thunder Bay City Council. A dedicated community advocate spanning more than a quarter century of municipal governance.", ShortSummary: "Veteran councillor, longest-serving member at the time.", Photo: "ruberto.jpg", Type: models.CouncillorTypeAtLarge},
		},
		Ward: []models.Councillor{
			{Name: "Andrew Foulds", Position: "Current River", Term: "3rd term", Summary: "Biology teacher at Westgate Collegiate with degrees from the University of Toronto and Queen's University. Championed the Boulevard Lake dam rehabilitation project, a defining infrastructure issue for Current River.", ShortSummary: "Biology teacher, Boulevard Lake advocate.", Photo: "foulds.jpg", Type: models.CouncillorTypeWard},
			{Name: "Joe Virdiramo", Position: "Westfort", Term: "6th term", Summary: "Long-serving Westfort ward representative and former Chair of the Thunder Bay District Health Unit. Advocated for the Children's Charter and Youth Centres Thunder Bay initiative in the Fort William Business District.", ShortSummary: "Long-serving Westfort representative, Health Unit chair.", Photo: "virdiramo.jpg", Type: models.CouncillorTypeWard},
			{Name: "Paul Pugh", Position: "McKellar", Term: "2nd term", Summary: "Holds a Master's degree in economics and is a past local union president at Bombardier (CAW Local 1075). Chaired the Poverty Reduction Strategy and advocated for improved housing and transit.", ShortSummary: "Economist and poverty reduction advocate.", Photo: "pugh.jpg", Type: models.CouncillorTypeWard},
			// TODO: verify Lori Paras ward — may not have been Westfort (Virdiramo held Westfort this term)
			{Name: "Lori Paras", Position: "Westfort", Term: "1st term", Summary: "Serial entrepreneur who owned Ruby Moon Catering & Lunch Bar and The Hub Bazaar, a business incubator on Victoria Avenue. Winner of the PARO Centre for Women's Enterprise Changemaker Award.", ShortSummary: "Entrepreneur and women's enterprise advocate.", Type: models.CouncillorTypeWard},
			{Name: "Peng You", Position: "Neebing", Term: "1st term", Summary: "Internationally recognized Tai Chi Master who moved to Thunder Bay from China in 1990. Founded the Peng You International Tai Chi Academy and earned the Thunder Bay Chamber Ambassador and Business Excellence Awards.", ShortSummary: "Tai Chi master, first elected to Neebing ward.", Photo: "you.jpg", Type: models.CouncillorTypeWard},
			// TODO: verify Frank Iozzo — no public records found for this councillor
			{Name: "Frank Iozzo", Position: "Northwood", Term: "5th term", Summary: "Long-serving Northwood ward representative.", ShortSummary: "Long-serving Northwood representative.", Type: models.CouncillorTypeWard},
			{Name: "Larry Hebert", Position: "Red River", Term: "6th term", Summary: "Served more than 20 years as General Manager of Thunder Bay Hydro before entering politics. A dedicated sports community builder who helped bring the World Junior Baseball Championship (2010) and U18 Baseball World Cup (2017) to Thunder Bay.", ShortSummary: "Former Thunder Bay Hydro GM, sports community builder.", Photo: "hebert.jpg", Type: models.CouncillorTypeWard},
		},
		KeyVotes: []models.KeyVote{
			{Issue: "Waterfront Development Phase 2", Result: "Passed", Vote: "10-3", MediaURL: "https://www.cbc.ca/news/canada/thunder-bay/thunder-bay-waterfront-plan-gets-council-approval-but-not-without-debate-1.3238234"},
			{Issue: "New Police Headquarters", Result: "Passed", Vote: "8-5"},
		},
		Stats: models.CouncilStats{
			MayorSalary:        "$82,000 -> $89,000",
			CouncillorSalary:   "$27,000 -> $29,000",
			TotalAnnual:        "$406,000 → $437,000",
			SalaryIncreaseNote: "~1.9%/yr avg",
			TermLength:         "4 years",
			CurrentTerm:        "2014–2018",
			NextElection:       "October 2018",
			Source:             models.SourceRef{URL: "https://www.tbnewswatch.com/local-news/council-salaries-made-public-875795"},
		},
	},
	2010: {
		Mayor: models.Councillor{
			Name:         "Keith Hobbs",
			Position:     "Mayor",
			Term:         "1st term",
			Summary:      "Former Thunder Bay Police Service officer who served 25 years before entering politics. Defeated incumbent mayor Lynn Peterson in the 2010 election, campaigning on accountability and fiscal restraint.",
			ShortSummary: "Former police officer, defeated incumbent on accountability platform.",
			Photo:        "hobbs.jpg",
			Type:         models.CouncillorTypeMayor,
		},
		AtLarge: []models.Councillor{
			{Name: "Trevor Giertuga", Term: "3rd term", Summary: "Deputy Superintendent of Programs at the Thunder Bay Correctional Centre. First elected in 2000, quickly establishing a reputation for constituent accessibility.", ShortSummary: "Correctional services, first elected in 2000.", Photo: "giertuga.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Mark Bentz", Term: "2nd term", Summary: "Professor at Confederation College's School of Aviation, Engineering Technology and Trades with degrees in Physics and Education. An early champion of the waterfront redevelopment that would become Prince Arthur's Landing.", ShortSummary: "Confederation College professor.", Photo: "bentz.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Iain Angus", Term: "3rd term", Summary: "Former NDP Member of Parliament for Thunder Bay–Atikokan (1984–1993) and Ontario MPP (1975). Elected to Thunder Bay City Council in 2003 and served as Chair of the Waterfront Development Committee and the District Social Services Administration Board.", ShortSummary: "Former NDP MP, waterfront and social services chair.", Photo: "angus.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Rebecca Johnson", Term: "1st term", Summary: "Owner of Rebecca Reports, a governance consulting firm. Hosted 'Community Clipboard' on Shaw Cable for 20 years. Served on more than 20 boards and committees including Lakehead University and Diversity Thunder Bay. Recipient of the Queen Elizabeth Diamond Jubilee Medal.", ShortSummary: "Governance consultant and community media host.", Photo: "johnson.jpg", Type: models.CouncillorTypeAtLarge},
			{Name: "Aldo Ruberto", Term: "6th term", Summary: "First elected in 1988, one of the longest continuously serving members of Thunder Bay City Council. A steady presence through decades of municipal governance and community advocacy.", ShortSummary: "Veteran councillor with decades of service.", Photo: "ruberto.jpg", Type: models.CouncillorTypeAtLarge},
		},
		Ward: []models.Councillor{
			{Name: "Andrew Foulds", Position: "Current River", Term: "2nd term", Summary: "Biology teacher at Westgate Collegiate. Began championing the Boulevard Lake dam rehabilitation early in his council career, a project that would define his tenure.", ShortSummary: "Biology teacher, early Boulevard Lake champion.", Photo: "foulds.jpg", Type: models.CouncillorTypeWard},
			{Name: "Joe Virdiramo", Position: "Westfort", Term: "5th term", Summary: "Long-serving Westfort ward representative and advocate for youth and children's services. Served as Chair of the Thunder Bay District Health Unit and championed the Youth Centres Thunder Bay initiative.", ShortSummary: "Long-serving Westfort representative.", Photo: "virdiramo.jpg", Type: models.CouncillorTypeWard},
			{Name: "Paul Pugh", Position: "McKellar", Term: "1st term", Summary: "Holds a Master's degree in economics and is a past local union president at Bombardier (CAW Local 1075). Won the McKellar ward seat in 2010 and went on to chair the Poverty Reduction Strategy.", ShortSummary: "Economist and union leader, first-term McKellar representative.", Photo: "pugh.jpg", Type: models.CouncillorTypeWard},
			// TODO: verify Ken Boshcoff ward in 2010 — also Virdiramo listed as Westfort this term
			{Name: "Ken Boshcoff", Position: "Westfort", Term: "1st term", Summary: "Lakehead University economics graduate with a Master's in Environmental Studies from York University. Previously served as mayor (1997–2003) and federal MP for Thunder Bay–Rainy River (2004–2008). Returned to municipal politics as a ward councillor in 2010.", ShortSummary: "Former mayor and federal MP, returned to council.", Photo: "boshcoff.jpg", Type: models.CouncillorTypeWard},
			{Name: "Linda Rydholm", Position: "Neebing", Term: "4th term", Summary: "Doctor of Chiropractic, school teacher, and dairy farmer who first won the Neebing ward seat in 1997. Served 12 years with the Federation of Canadian Municipalities and earned the Queen's Diamond Jubilee Medal for her community service.", ShortSummary: "Chiropractor and long-serving Neebing representative.", Photo: "rydholm.jpg", Type: models.CouncillorTypeWard},
			// TODO: verify Frank Iozzo — no public records found for this councillor
			{Name: "Frank Iozzo", Position: "Northwood", Term: "4th term", Summary: "Veteran Northwood ward representative.", ShortSummary: "Veteran Northwood representative.", Type: models.CouncillorTypeWard},
			{Name: "Larry Hebert", Position: "Red River", Term: "5th term", Summary: "Served more than 20 years as General Manager of Thunder Bay Hydro. A lifelong Fort William native who starred in basketball at Fort William Collegiate and later helped bring international baseball championships to Thunder Bay.", ShortSummary: "Former Thunder Bay Hydro GM, sports community builder.", Photo: "hebert.jpg", Type: models.CouncillorTypeWard},
		},
		KeyVotes: []models.KeyVote{
			{Issue: "Prince Arthur's Landing Waterfront", Result: "Passed", Vote: "9-4"},
			{Issue: "Thunder Bay Expressway Widening", Result: "Deferred", Vote: "-"},
		},
		Stats: models.CouncilStats{
			MayorSalary:      "$83,921",
			CouncillorSalary: "$28,763",
			TotalAnnual:      "~$430,000",
			TermLength:       "4 years",
			CurrentTerm:      "2010–2014",
			NextElection:     "October 2014",
			Source: models.SourceRef{
				URL:  "https://www.tbnewswatch.com/local-news/salaries-slide-390625#:~:text=Councillor%20base%20salaries%20in%202011%20were%20%2428%2C763%20apiece%2C%20plus%20taxable%20benefits%20that%20averaged%20%245%2C665%20per%20person.%20The%20mayor%E2%80%99s%20base%20salary%20was%20%2483%2C921.",
				Note: "2011 figures",
			},
		},
	},
}
