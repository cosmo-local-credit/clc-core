package store

import (
	"context"
	"testing"
	"time"
)

func TestReportWriteLifecycleReplacesRelationsAndPreservesAuthorship(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	var firstTag, secondTag string
	rows, err := tx.Query(ctx, `SELECT value FROM token_tag ORDER BY value LIMIT 2`)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan tag: %v", err)
		}
		tags = append(tags, tag)
	}
	rows.Close()
	if len(tags) != 2 {
		t.Fatalf("tag vocabulary has %d rows, want at least 2", len(tags))
	}
	firstTag, secondTag = tags[0], tags[1]

	voucherID := seedMarketVoucher(t, tx, -1.29, 36.82, true)
	author := "0x0000000000000000000000000000000000000011"
	modifier := "0x0000000000000000000000000000000000000022"
	caption := "First"
	filename := "field-notes.pdf"
	from := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Hour)

	initial := ReportWrite{
		Title:       "Initial post",
		Body:        "## Initial",
		Description: "Initial description",
		PeriodFrom:  &from,
		PeriodTo:    &to,
		Tags:        []string{firstTag},
		VoucherIDs:  []uint64{voucherID},
		Images: []ReportImage{
			{URL: "https://content.example/first.jpg", Caption: &caption},
		},
		Attachments: []ReportAttachment{
			{Kind: "FILE", URL: "https://content.example/field-notes.pdf", Filename: &filename},
		},
	}
	id, err := pg.CreateReport(ctx, tx, initial, author, "SUBMITTED")
	if err != nil {
		t.Fatalf("create report: %v", err)
	}
	if err := pg.ReplaceReportRelations(ctx, tx, id, initial); err != nil {
		t.Fatalf("write initial relations: %v", err)
	}

	created, err := pg.GetReportForMutation(ctx, tx, id)
	if err != nil {
		t.Fatalf("get created report: %v", err)
	}
	if created == nil || created.AuthorAddr != author || created.Status != "SUBMITTED" || len(created.Tags) != 1 ||
		len(created.Vouchers) != 1 || len(created.Images) != 1 || len(created.Attachments) != 1 {
		t.Fatalf("created report = %+v", created)
	}
	lockedAuthor, err := pg.LockReportAuthor(ctx, tx, id)
	if err != nil || lockedAuthor != author {
		t.Fatalf("locked author = %q, err = %v", lockedAuthor, err)
	}

	updatedWrite := ReportWrite{
		Title:       "Updated post",
		Body:        "## Updated",
		Description: "Updated description",
		Tags:        []string{secondTag},
		Images: []ReportImage{
			{URL: "https://content.example/replacement.jpg"},
		},
	}
	resubmitted := ReportStatusSubmitted
	if err := pg.UpdateReport(ctx, tx, id, updatedWrite, modifier, &resubmitted); err != nil {
		t.Fatalf("update report: %v", err)
	}
	if err := pg.ReplaceReportRelations(ctx, tx, id, updatedWrite); err != nil {
		t.Fatalf("replace report relations: %v", err)
	}

	updated, err := pg.GetReportForMutation(ctx, tx, id)
	if err != nil {
		t.Fatalf("get updated report: %v", err)
	}
	if updated == nil || updated.Title != "Updated post" || updated.AuthorAddr != author || updated.Status != "SUBMITTED" {
		t.Fatalf("updated report = %+v", updated)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != secondTag || len(updated.Vouchers) != 0 ||
		len(updated.Images) != 1 || len(updated.Attachments) != 0 {
		t.Fatalf("updated relations = tags:%v vouchers:%v images:%v attachments:%v",
			updated.Tags, updated.Vouchers, updated.Images, updated.Attachments)
	}

	approver := "0x0000000000000000000000000000000000000033"
	if err := pg.ApproveReport(ctx, tx, id, approver); err != nil {
		t.Fatalf("approve report: %v", err)
	}

	// An author's edit of an approved post resubmits it and takes it out of the
	// public read, which is what stops editing being a way to substitute
	// unreviewed content for approved content.
	updatedWrite.Title = "Edited after approval"
	if err := pg.UpdateReport(ctx, tx, id, updatedWrite, author, &resubmitted); err != nil {
		t.Fatalf("author edit of approved report: %v", err)
	}
	if public, err := pg.GetReport(ctx, tx, id); err != nil || public != nil {
		t.Fatalf("public read after author edit = %+v, err = %v", public, err)
	}
	edited, err := pg.GetReportForMutation(ctx, tx, id)
	if err != nil || edited == nil || edited.Status != ReportStatusSubmitted ||
		edited.Title != "Edited after approval" {
		t.Fatalf("report after author edit = %+v, err = %v", edited, err)
	}

	// An operator's edit passes no status, so COALESCE preserves whatever the
	// row holds: approving and then correcting must not unpublish.
	if err := pg.ApproveReport(ctx, tx, id, approver); err != nil {
		t.Fatalf("reapprove report: %v", err)
	}
	updatedWrite.Title = "Corrected by an operator"
	if err := pg.UpdateReport(ctx, tx, id, updatedWrite, approver, nil); err != nil {
		t.Fatalf("admin edit of approved report: %v", err)
	}
	approved, err := pg.GetReport(ctx, tx, id)
	if err != nil || approved == nil || approved.Status != ReportStatusApproved ||
		approved.Title != "Corrected by an operator" {
		t.Fatalf("approved report after admin edit = %+v, err = %v", approved, err)
	}

	var storedAuthor, storedModifier, status string
	if err := tx.QueryRow(ctx,
		`SELECT author_address, modifier_address, status FROM reports WHERE id = $1`, id,
	).Scan(&storedAuthor, &storedModifier, &status); err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if storedAuthor != author || storedModifier != approver || status != ReportStatusApproved {
		t.Fatalf("provenance = author:%q modifier:%q status:%q", storedAuthor, storedModifier, status)
	}

	if err := pg.DeleteReport(ctx, tx, id); err != nil {
		t.Fatalf("delete report: %v", err)
	}
	deleted, err := pg.GetReportForMutation(ctx, tx, id)
	if err != nil || deleted != nil {
		t.Fatalf("get deleted report = %+v, err = %v", deleted, err)
	}
}

func TestGetReportForMutationDoesNotBroadenPublicDetail(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	id := seedReport(t, tx, "DRAFT", time.Now().UTC(), "Draft body")
	public, err := pg.GetReport(ctx, tx, id)
	if err != nil {
		t.Fatalf("get public report: %v", err)
	}
	if public != nil {
		t.Fatalf("public draft = %+v, want nil", public)
	}
	mutation, err := pg.GetReportForMutation(ctx, tx, id)
	if err != nil || mutation == nil {
		t.Fatalf("mutation draft = %+v, err = %v", mutation, err)
	}
	if err := pg.ApproveReport(ctx, tx, id, "0x0000000000000000000000000000000000000033"); err != nil {
		t.Fatalf("approve draft: %v", err)
	}
	public, err = pg.GetReport(ctx, tx, id)
	if err != nil || public == nil || public.Status != "APPROVED" {
		t.Fatalf("approved public report = %+v, err = %v", public, err)
	}
}
