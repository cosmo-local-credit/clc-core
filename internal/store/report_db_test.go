package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedReport inserts one report at a chosen updated_at. The value is set in the
// INSERT rather than a follow-up UPDATE on purpose: update_reports_timestamp is
// a BEFORE UPDATE trigger that would rewrite it to now, collapsing every
// fixture onto one instant and silently destroying the ordering under test.
//
// `body` is markdown since migration 010, not the editor's JSONB document.
func seedReport(t *testing.T, tx pgx.Tx, status string, updatedAt time.Time, body string) uint64 {
	t.Helper()
	ctx := context.Background()

	var id uint64
	err := tx.QueryRow(ctx, `
		INSERT INTO reports (title, body, description, author_address, status,
		                     period_from, period_to, updated_at)
		VALUES ('Fixture Report', $1, 'Fixture description',
		        '0x0000000000000000000000000000000000000009', $2,
		        '2026-07-01T00:00:00Z', '2026-07-02T00:00:00Z', $3)
		RETURNING id`, body, status, updatedAt).Scan(&id)
	if err != nil {
		t.Fatalf("seed report: %v", err)
	}
	return id
}

func TestReportFeedReturnsOnlyApprovedRowsNewestUpdateFirst(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	// Insert oldest-updated first so id order and updated_at order disagree,
	// which is exactly how the imported catalog behaves.
	oldest := seedReport(t, tx, "APPROVED", base, "")
	newest := seedReport(t, tx, "APPROVED", base.Add(2*time.Hour), "")
	middle := seedReport(t, tx, "APPROVED", base.Add(time.Hour), "")
	seedReport(t, tx, "DRAFT", base.Add(3*time.Hour), "")
	seedReport(t, tx, "SUBMITTED", base.Add(4*time.Hour), "")
	seedReport(t, tx, "REJECTED", base.Add(5*time.Hour), "")

	rows, err := pg.ListReports(ctx, tx, 50)
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}

	seen := map[uint64]int{}
	for index, row := range rows {
		seen[row.ID] = index
	}
	for _, id := range []uint64{oldest, newest, middle} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("approved report %d missing from the feed", id)
		}
	}
	if seen[newest] >= seen[middle] || seen[middle] >= seen[oldest] {
		t.Fatalf("feed is not ordered by updated_at: newest=%d middle=%d oldest=%d",
			seen[newest], seen[middle], seen[oldest])
	}

	for index := 1; index < len(rows); index++ {
		if rows[index].UpdatedAt.After(rows[index-1].UpdatedAt) {
			t.Fatalf("row %d updated after the row before it", index)
		}
	}
}

func TestReportFeedKeysetCoversEveryRowExactlyOnce(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// A shared timestamp is the case an updated_at-only cursor gets wrong: the
	// id half of the pair is what keeps identical instants from repeating.
	shared := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	wanted := map[uint64]bool{}
	for range 5 {
		wanted[seedReport(t, tx, "APPROVED", shared, "")] = false
	}
	wanted[seedReport(t, tx, "APPROVED", shared.Add(time.Minute), "")] = false

	var cursor ReportCursor
	seenTotal := 0
	for page := range 10 {
		var rows []Report
		var err error
		if page == 0 {
			rows, err = pg.ListReports(ctx, tx, 2)
		} else {
			rows, err = pg.ListReportsNext(ctx, tx, cursor, 2)
		}
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if already, tracked := wanted[row.ID]; tracked {
				if already {
					t.Fatalf("report %d appeared on more than one page", row.ID)
				}
				wanted[row.ID] = true
			}
			seenTotal++
		}
		cursor = ReportCursor{UpdatedAt: rows[len(rows)-1].UpdatedAt, ID: rows[len(rows)-1].ID}
		if len(rows) < 2 {
			break
		}
	}

	for id, found := range wanted {
		if !found {
			t.Fatalf("report %d was never returned across the paged feed", id)
		}
	}
	if seenTotal == 0 {
		t.Fatal("paging returned nothing")
	}
}

// The detail route reads media from its own relations rather than re-parsing a
// document, so what it returns is whatever write time put there, in ordinal
// order — including the caption and intrinsic size the old query dropped.
func TestReportDetailReturnsMediaAndFormFromTheirOwnRelations(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	id := seedReport(t, tx, "APPROVED",
		time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC),
		"## Cycle report\n\nEight members attended.")

	_, err := tx.Exec(ctx, `
		UPDATE reports SET host_name = 'Wendo Group', participants = '7',
		                   time_taken = '1 Hour'
		WHERE id = $1`, id)
	if err != nil {
		t.Fatalf("set form fields: %v", err)
	}

	// Inserted out of ordinal order to prove the query orders, not the insert.
	_, err = tx.Exec(ctx, `
		INSERT INTO report_images (report_id, ordinal, url, width, height,
		                           max_width, max_height, byte_size, caption)
		VALUES ($1, 2, 'https://content.example/second.jpg',
		        NULL, NULL, NULL, NULL, NULL, NULL),
		       ($1, 1, 'https://content.example/first.jpg',
		        267, 1000, 747, 1600, 2078440, 'Breaking bulk')`, id)
	if err != nil {
		t.Fatalf("seed images: %v", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO report_attachments (report_id, ordinal, kind, url, filename,
		                                byte_size)
		VALUES ($1, 1, 'FILE', 'https://content.example/report.pdf',
		        'Prestação de Contas.pdf', 102929),
		       ($1, 2, 'EMBED', 'https://youtu.be/MkyWW2l-hbY', NULL, NULL)`, id)
	if err != nil {
		t.Fatalf("seed attachments: %v", err)
	}

	report, err := pg.GetReport(ctx, tx, id)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if report == nil {
		t.Fatal("approved report was not found")
	}

	if report.Body == nil || *report.Body != "## Cycle report\n\nEight members attended." {
		t.Fatalf("body = %v, want the stored markdown", report.Body)
	}
	if report.HostName == nil || *report.HostName != "Wendo Group" {
		t.Fatalf("hostName = %v, want Wendo Group", report.HostName)
	}
	if report.Participants == nil || *report.Participants != "7" {
		t.Fatalf("participants = %v, want 7", report.Participants)
	}
	if report.TimeTaken == nil || *report.TimeTaken != "1 Hour" {
		t.Fatalf("timeTaken = %v, want 1 Hour", report.TimeTaken)
	}

	if len(report.Images) != 2 {
		t.Fatalf("images = %d, want 2: %+v", len(report.Images), report.Images)
	}
	if report.Images[0].URL != "https://content.example/first.jpg" {
		t.Fatalf("first image = %q, want ordinal order", report.Images[0].URL)
	}
	if report.Images[1].URL != "https://content.example/second.jpg" {
		t.Fatalf("second image = %q", report.Images[1].URL)
	}
	if report.Images[0].Width == nil || *report.Images[0].Width != 267 {
		t.Fatalf("first image width = %v, want the display box 267", report.Images[0].Width)
	}
	// The display box and the intrinsic size are not a scaled pair, so both are
	// carried rather than one being derived from the other.
	if report.Images[0].MaxWidth == nil || *report.Images[0].MaxWidth != 747 {
		t.Fatalf("first image maxWidth = %v, want 747", report.Images[0].MaxWidth)
	}
	if report.Images[0].Caption == nil || *report.Images[0].Caption != "Breaking bulk" {
		t.Fatalf("first image caption = %v, want Breaking bulk", report.Images[0].Caption)
	}
	if report.Images[1].Width != nil {
		t.Fatalf("second image width = %v, want nil when the editor stored none",
			report.Images[1].Width)
	}

	if len(report.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2: %+v", len(report.Attachments), report.Attachments)
	}
	if report.Attachments[0].Kind != "FILE" || report.Attachments[0].Filename == nil {
		t.Fatalf("first attachment = %+v, want a named FILE", report.Attachments[0])
	}
	if report.Attachments[1].Kind != "EMBED" || report.Attachments[1].Filename != nil {
		t.Fatalf("second attachment = %+v, want an unnamed EMBED", report.Attachments[1])
	}
}

// A base64 data URI reaching report_images would defeat the migration: one such
// node is ~900 KB. The exclusion is a storage invariant now, not a filter every
// request re-applies, so it has to hold against a direct INSERT.
func TestReportImagesRejectNonHttpUrls(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	id := seedReport(t, tx, "APPROVED", time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC), "")

	for _, url := range []string{
		"data:image/jpeg;base64,AAAA",
		"#eWT2_eyzG9irIxBnqtKuA", // unresolved editor upload placeholder
		"",
		"ftp://content.example/x.jpg",
	} {
		// A rejected INSERT aborts the whole transaction, so each attempt runs
		// inside its own savepoint.
		if _, err := tx.Exec(ctx, "SAVEPOINT bad_url"); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO report_images (report_id, ordinal, url)
			VALUES ($1, 1, $2)`, id, url)
		if err == nil {
			t.Fatalf("report_images accepted a non-http url: %q", url)
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT bad_url"); err != nil {
			t.Fatalf("rollback to savepoint: %v", err)
		}
	}
}

// The parser functions are the single write-time parser: this migration used
// them to convert 4,632 rows and the federation importer calls them on every
// re-import. They are exercised against the node shapes the live corpus
// actually contains.
func TestReportBodyParserExtractsMediaAndProse(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	const body = `[
		{"type":"h2","children":[{"text":"Cycle report","bold":true}]},
		{"type":"p","children":[
			{"text":"Eight members attended "},
			{"type":"a","url":"https://example.org/c/abc123","target":"_blank",
			 "children":[{"text":"abc123","color":"#000000"}]},
			{"text":" today."}
		]},
		{"type":"field-report-form","children":[{"text":""}],
		 "formData":{"hostName":"Wendo Group","participants":"7",
		             "timeTaken":"1 Hour","description":"redundant"}},
		{"type":"cloud_image","url":"https://content.example/first.jpg",
		 "width":267,"height":1000,"maxWidth":747,"maxHeight":1600,
		 "bytes":2078440,"caption":[{"text":"Breaking bulk"}]},
		{"type":"img","url":"data:image/jpeg;base64,AAAA"},
		{"type":"img"},
		{"type":"cloud_image","url":"#eWT2_eyzG9irIxBnqtKuA"},
		{"type":"cloud_image","url":"https://content.example/second.jpg"},
		{"type":"cloud_attachment","url":"https://content.example/r.pdf",
		 "filename":"Report.pdf","bytes":102929},
		{"type":"media_embed","url":"https://youtu.be/MkyWW2l-hbY"},
		{"type":"cloud_attachment","url":"#JXa465C_iPWjdHw3tn4uo",
		 "filename":"VID.mp4","bytes":2444990},
		{"type":"hr","children":[{"text":""}]},
		{"type":"p","children":[{"text":""}]},
		{"type":"p","children":[{"text":"\n"}]}
	]`

	var markdown string
	if err := tx.QueryRow(ctx,
		`SELECT report_body_markdown($1::JSONB)`, body).Scan(&markdown); err != nil {
		t.Fatalf("report_body_markdown: %v", err)
	}

	// Heading level, emphasis and the link URL all survive. The URL exists only
	// in the node attribute, so plain-text flattening would have lost it.
	const wantMarkdown = "## **Cycle report**\n\n" +
		"Eight members attended [abc123](https://example.org/c/abc123) today.\n\n" +
		"---"
	if markdown != wantMarkdown {
		t.Fatalf("markdown =\n%q\nwant\n%q", markdown, wantMarkdown)
	}

	// Media and form nodes are not prose: they have their own homes, and leaving
	// them in the body would store the same fact twice.
	for _, unwanted := range []string{"content.example", "Wendo Group", "base64"} {
		if strings.Contains(markdown, unwanted) {
			t.Fatalf("markdown leaked %q: %q", unwanted, markdown)
		}
	}

	type image struct {
		ordinal                            int32
		url                                string
		width, height, maxWidth, maxHeight *int32
		byteSize                           *int64
		caption                            *string
	}
	rows, err := tx.Query(ctx, `
		SELECT ordinal, url, width, height, max_width, max_height, byte_size,
		       caption
		FROM report_body_images($1::JSONB) ORDER BY ordinal`, body)
	if err != nil {
		t.Fatalf("report_body_images: %v", err)
	}
	var images []image
	for rows.Next() {
		var i image
		if err := rows.Scan(&i.ordinal, &i.url, &i.width, &i.height, &i.maxWidth,
			&i.maxHeight, &i.byteSize, &i.caption); err != nil {
			t.Fatalf("scan image: %v", err)
		}
		images = append(images, i)
	}
	rows.Close()

	// Only the two http images survive: the data URI, the two "#nanoid" upload
	// placeholders and the url-less node are all dropped, and the survivors are
	// renumbered 1..n in document order rather than keeping their node index.
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2: %+v", len(images), images)
	}
	if images[0].url != "https://content.example/first.jpg" || images[0].ordinal != 1 {
		t.Fatalf("first image = %+v", images[0])
	}
	if images[1].url != "https://content.example/second.jpg" || images[1].ordinal != 2 {
		t.Fatalf("second image = %+v", images[1])
	}
	if images[0].width == nil || *images[0].width != 267 {
		t.Fatalf("width = %v, want 267", images[0].width)
	}
	if images[0].maxHeight == nil || *images[0].maxHeight != 1600 {
		t.Fatalf("maxHeight = %v, want 1600", images[0].maxHeight)
	}
	// `caption` is a rich-text array in the source, not a string.
	if images[0].caption == nil || *images[0].caption != "Breaking bulk" {
		t.Fatalf("caption = %v, want Breaking bulk", images[0].caption)
	}
	if images[1].caption != nil {
		t.Fatalf("second caption = %v, want nil", images[1].caption)
	}

	var kind, url string
	var filename *string
	var byteSize *int64
	var ordinal int32
	attachments, err := tx.Query(ctx, `
		SELECT ordinal, kind, url, filename, byte_size
		FROM report_body_attachments($1::JSONB) ORDER BY ordinal`, body)
	if err != nil {
		t.Fatalf("report_body_attachments: %v", err)
	}
	var got []string
	for attachments.Next() {
		if err := attachments.Scan(&ordinal, &kind, &url, &filename, &byteSize); err != nil {
			t.Fatalf("scan attachment: %v", err)
		}
		got = append(got, kind+" "+url)
	}
	attachments.Close()

	// The placeholder-url attachment is dropped; the embed is distinguished from
	// the uploaded file so a client can tell a player from a download.
	if len(got) != 2 ||
		got[0] != "FILE https://content.example/r.pdf" ||
		got[1] != "EMBED https://youtu.be/MkyWW2l-hbY" {
		t.Fatalf("attachments = %v", got)
	}

	var hostName, participants, timeTaken *string
	err = tx.QueryRow(ctx, `
		SELECT host_name, participants, time_taken
		FROM report_body_form($1::JSONB)`, body).
		Scan(&hostName, &participants, &timeTaken)
	if err != nil {
		t.Fatalf("report_body_form: %v", err)
	}
	if hostName == nil || *hostName != "Wendo Group" {
		t.Fatalf("hostName = %v", hostName)
	}
	if participants == nil || *participants != "7" {
		t.Fatalf("participants = %v", participants)
	}
	if timeTaken == nil || *timeTaken != "1 Hour" {
		t.Fatalf("timeTaken = %v", timeTaken)
	}
}

// Seven imported rows carry two form nodes, and on two of them the second
// differs, so the rule has to be deterministic: the first node in document
// order that has any value at all. A leading blank form must not win.
func TestReportBodyFormSkipsBlankFormNodes(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	const body = `[
		{"type":"field-report-form","formData":{"hostName":"  ",
		 "participants":"","timeTaken":" "}},
		{"type":"field-report-form","formData":{"hostName":"Greenpill Brasil",
		 "participants":"21","timeTaken":"3 months"}},
		{"type":"field-report-form","formData":{"hostName":"Later Group",
		 "participants":"180","timeTaken":"4 hours"}}
	]`

	var hostName, participants, timeTaken *string
	err := tx.QueryRow(ctx, `
		SELECT host_name, participants, time_taken
		FROM report_body_form($1::JSONB)`, body).
		Scan(&hostName, &participants, &timeTaken)
	if err != nil {
		t.Fatalf("report_body_form: %v", err)
	}
	if hostName == nil || *hostName != "Greenpill Brasil" {
		t.Fatalf("hostName = %v, want the first form carrying values", hostName)
	}
	if timeTaken == nil || *timeTaken != "3 months" {
		t.Fatalf("timeTaken = %v, want 3 months", timeTaken)
	}
}

// Bare btrim() strips spaces only. A node holding just a newline therefore
// reads as non-empty, and the emphasis markers end up wrapped around the
// whitespace instead of the word.
func TestReportBodyMarkdownHandlesWhitespaceOnlyAndPaddedText(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	for _, tc := range []struct{ name, body, want string }{
		{
			name: "whitespace-only nodes produce no prose",
			body: `[{"type":"p","children":[{"text":"\n"}]},
			        {"type":"p","children":[{"text":"  \t "}]},
			        {"type":"p","children":[{"text":""}]}]`,
			want: "",
		},
		{
			name: "emphasis markers sit inside surrounding whitespace",
			body: `[{"type":"p","children":[{"text":"a "},
			        {"text":"bold ","bold":true},{"text":"b"}]}]`,
			want: "a **bold** b",
		},
		{
			name: "newline-padded bold is not duplicated",
			body: `[{"type":"p","children":[{"text":"\nbold\n","bold":true}]}]`,
			want: "**bold**",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if err := tx.QueryRow(ctx,
				`SELECT report_body_markdown($1::JSONB)`, tc.body).Scan(&got); err != nil {
				t.Fatalf("report_body_markdown: %v", err)
			}
			if got != tc.want {
				t.Fatalf("markdown = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReportDetailHidesUnapprovedReports(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	for _, status := range []string{"DRAFT", "SUBMITTED", "REJECTED"} {
		id := seedReport(t, tx, status, time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC), "")

		report, err := pg.GetReport(ctx, tx, id)
		if err != nil {
			t.Fatalf("%s report lookup: %v", status, err)
		}
		if report != nil {
			t.Fatalf("%s report is readable through the public detail query", status)
		}
	}
}

// A base64 data URI in reports.image_url is served to every feed row, so the
// queries gate it. Six approved rows still hold one, up to 876 KB; nulling them
// would leave four reports with no image at all, so the gate is at read time.
func TestReportQueriesHideDataUriCoverImages(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	id := seedReport(t, tx, "APPROVED", time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC), "")

	// The six live rows predate migration 021's reports_image_url_http check, and
	// that check is NOT VALID precisely so they survive it — but it still refuses
	// a new data URI, which is the whole point of adding it. Dropping it inside
	// this transaction is how the fixture reproduces a row that can no longer be
	// created; the rollback puts it back.
	if _, err := tx.Exec(ctx, `
		ALTER TABLE reports DROP CONSTRAINT IF EXISTS reports_image_url_http`); err != nil {
		t.Fatalf("drop url check for the legacy fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE reports SET image_url = 'data:image/jpeg;base64,/9j/4AAQSkZJRg'
		WHERE id = $1`, id); err != nil {
		t.Fatalf("set data uri cover: %v", err)
	}

	report, err := pg.GetReport(ctx, tx, id)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if report == nil {
		t.Fatal("approved report was not found")
	}
	if report.ImageURL != nil {
		t.Fatalf("detail imageUrl = %q, want nil for a data URI", *report.ImageURL)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE reports SET image_url = 'https://content.example/cover.jpg'
		WHERE id = $1`, id); err != nil {
		t.Fatalf("set http cover: %v", err)
	}
	report, err = pg.GetReport(ctx, tx, id)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if report.ImageURL == nil || *report.ImageURL != "https://content.example/cover.jpg" {
		t.Fatalf("detail imageUrl = %v, want the http url through", report.ImageURL)
	}
}
