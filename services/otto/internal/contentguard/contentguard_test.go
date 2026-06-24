package contentguard

import "testing"

func TestScanBlocks(t *testing.T) {
	cases := []struct {
		name string
		text string
		cat  Category
	}{
		{"profanity", "this is fucking broken", CategoryProfanity},
		{"slur", "you absolute retard", CategoryProfanity},
		{"hindi romanised", "Randi", CategoryProfanity},
		{"hindi sentence", "tu chutiya hai", CategoryProfanity},
		{"spanish", "eres una puta", CategoryProfanity},
		{"devanagari", "तुम रंडी हो", CategoryProfanity},
		{"email", "reach me at john.doe@example.com please", CategoryPII},
		{"phone spaced", "call me on 0412 345 678", CategoryPII},
		{"phone plain", "my mobile is 2345678901", CategoryPII},
		{"phone intl", "ring +1 (234) 567-8900", CategoryPII},
		{"card", "my card is 4111 1111 1111 1111", CategoryPII},
	}
	for _, c := range cases {
		r := Scan(c.text)
		if r.Allowed || r.Category != c.cat {
			t.Errorf("%s: want blocked %s, got allowed=%v cat=%s", c.name, c.cat, r.Allowed, r.Category)
		}
		if r.Reason == "" {
			t.Errorf("%s: blocked result missing reason", c.name)
		}
	}
}

func TestScanAllows(t *testing.T) {
	ok := []string{
		"Where is my order CS-260623-3ADS?",
		"The total was $1,234.56 but I was charged twice",
		"Can the assistant help me classify this first-class issue?",
		"My order number is 260623",
		"I need help with order #4821",
		"What is the refund policy?",
		"This is a stupid bug that crippled my checkout", // mild words not blocked
		"",
	}
	for _, txt := range ok {
		if r := Scan(txt); !r.Allowed {
			t.Errorf("want allowed, got blocked (%s): %q", r.Category, txt)
		}
	}
}
