package store

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeViewPinsNodeAndPathAcrossConcurrentTrash(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	parent, err := s.Mkdir(ctx, s.RootID(), "archive")
	require.NoError(t, err)
	node, err := s.Mkdir(ctx, parent.ID, "records")
	require.NoError(t, err)

	trashStarted := make(chan struct{})
	trashed := make(chan error, 1)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	snapshot, err := nodeByIDTx(tx, node.ID)
	require.NoError(t, err)

	go func() {
		close(trashStarted)
		_, _, trashErr := s.Trash(context.Background(), node.ID, node.Revision)
		trashed <- trashErr
	}()
	<-trashStarted
	view, err := nodeViewForNode(ctx, tx, snapshot)
	require.NoError(t, err)
	assert.Nil(t, view.Node.TrashedAt)
	assert.Equal(t, "/archive/records", view.Path)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-trashed)

	after, err := s.NodeViewByID(ctx, node.ID)
	require.NoError(t, err)
	assert.NotNil(t, after.Node.TrashedAt)
	assert.Empty(t, after.Path)
}

func TestMkdirAndLookup(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	docs, err := s.Mkdir(ctx, s.RootID(), "docs")
	require.NoError(t, err)
	assert.Equal(t, "docs", docs.Name)
	assert.True(t, docs.IsDir())
	assert.Equal(t, int64(1), docs.Revision)

	byID, err := s.NodeByID(ctx, docs.ID)
	require.NoError(t, err)
	assert.Equal(t, docs.ID, byID.ID)

	byPath, err := s.NodeByPath(ctx, "/docs")
	require.NoError(t, err)
	assert.Equal(t, docs.ID, byPath.ID)

	root, err := s.NodeByPath(ctx, "/")
	require.NoError(t, err)
	assert.Equal(t, s.RootID(), root.ID)

	_, err = s.NodeByPath(ctx, "/nope")
	require.ErrorIs(t, err, ErrNotFound)

	p, err := s.Path(ctx, docs.ID)
	require.NoError(t, err)
	assert.Equal(t, "/docs", p)
}

func TestPathOnMissingNodeReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	_, err := s.Path(ctx, 99999)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestMkdirRejectsCollisionAndBadNames(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	_, err := s.Mkdir(ctx, s.RootID(), "docs")
	require.NoError(t, err)
	_, err = s.Mkdir(ctx, s.RootID(), "docs")
	require.ErrorIs(t, err, ErrExists)
	_, err = s.Mkdir(ctx, s.RootID(), "a/b")
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestMkdirPathCreatesOneExactDirectory(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	projects, err := s.Mkdir(ctx, s.RootID(), "Projects")
	require.NoError(t, err)
	created, path, err := s.MkdirPath(ctx, "/Projects/Reports/")
	require.NoError(t, err)
	assert.Equal(t, "/Projects/Reports", path)
	assert.Equal(t, "Reports", created.Name)
	assert.Equal(t, projects.ID, *created.ParentID)

	resolved, err := s.NodeByPath(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, created.ID, resolved.ID)

	_, _, err = s.MkdirPath(ctx, "/Projects/Reports")
	require.ErrorIs(t, err, ErrExists)
	_, _, err = s.MkdirPath(ctx, "/Missing/Reports")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.MkdirPath(ctx, "/")
	require.ErrorIs(t, err, ErrExists)
	_, _, err = s.MkdirPath(ctx, "Projects/Relative")
	require.ErrorIs(t, err, ErrInvalidName)
	_, _, err = s.MkdirPath(ctx, "/Projects/../Reports")
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestMkdirPathRejectsFileParent(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateFile(
		t.Context(), s.RootID(), "document.txt", fakeHash("mkdir-parent"), 1, "text/plain",
	)
	require.NoError(t, err)

	_, _, err = s.MkdirPath(t.Context(), "/document.txt/child")
	require.ErrorIs(t, err, ErrNotDir)
}

func TestMkdirBumpsParentRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	before, err := s.NodeByID(ctx, s.RootID())
	require.NoError(t, err)
	_, err = s.Mkdir(ctx, s.RootID(), "docs")
	require.NoError(t, err)
	after, err := s.NodeByID(ctx, s.RootID())
	require.NoError(t, err)
	assert.Equal(t, before.Revision+1, after.Revision)
}

func TestMkdirAllCreatesIntermediates(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	leaf, err := s.MkdirAll(ctx, "/a/b/c")
	require.NoError(t, err)
	p, err := s.Path(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, "/a/b/c", p)

	// Idempotent.
	again, err := s.MkdirAll(ctx, "/a/b/c")
	require.NoError(t, err)
	assert.Equal(t, leaf.ID, again.ID)
}

func TestMkdirAllConcurrent(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	const n = 8
	var wg sync.WaitGroup
	ids := make([]int64, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			leaf, err := s.MkdirAll(ctx, "/a/b/c")
			ids[i] = leaf.ID
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		assert.Equal(t, ids[0], ids[i])
	}

	kids, err := s.Children(ctx, s.RootID())
	require.NoError(t, err)
	require.Len(t, kids, 1)
	assert.Equal(t, "a", kids[0].Name)
}

func TestChildrenSortedDirsFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	_, err := s.Mkdir(ctx, s.RootID(), "zdir")
	require.NoError(t, err)
	_, err = s.Mkdir(ctx, s.RootID(), "adir")
	require.NoError(t, err)

	kids, err := s.Children(ctx, s.RootID())
	require.NoError(t, err)
	require.Len(t, kids, 2)
	assert.Equal(t, "adir", kids[0].Name)
	assert.Equal(t, "zdir", kids[1].Name)

	_, err = s.Children(ctx, kids[0].ID)
	require.NoError(t, err)
}

func TestChildrenPageBoundsResultsAndPreservesTotal(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	_, err := s.Mkdir(ctx, s.RootID(), "zdir")
	require.NoError(t, err)
	_, err = s.Mkdir(ctx, s.RootID(), "adir")
	require.NoError(t, err)
	for _, name := range []string{"bravo.txt", "alpha.txt"} {
		_, err = s.CreateFile(ctx, s.RootID(), name, strings.Repeat("a", 64), 1, "text/plain")
		require.NoError(t, err)
	}

	first, total, err := s.ChildrenPage(ctx, s.RootID(), 3, 0)
	require.NoError(t, err)
	assert.Equal(t, 4, total)
	require.Len(t, first, 3)
	assert.Equal(t, []string{"adir", "zdir", "alpha.txt"}, []string{
		first[0].Name, first[1].Name, first[2].Name,
	})

	last, total, err := s.ChildrenPage(ctx, s.RootID(), 3, 3)
	require.NoError(t, err)
	assert.Equal(t, 4, total)
	require.Len(t, last, 1)
	assert.Equal(t, "bravo.txt", last[0].Name)

	empty, total, err := s.ChildrenPage(ctx, s.RootID(), 3, 4)
	require.NoError(t, err)
	assert.Equal(t, 4, total)
	assert.Empty(t, empty)

	_, _, err = s.ChildrenPage(ctx, first[2].ID, 3, 0)
	require.ErrorIs(t, err, ErrNotDir)
	_, _, err = s.ChildrenPage(ctx, 1<<62, 3, 0)
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.ChildrenPage(ctx, s.RootID(), 0, 0)
	require.Error(t, err)
	_, _, err = s.ChildrenPage(ctx, s.RootID(), 3, -1)
	require.Error(t, err)
}

func TestDocumentPageListsLiveFilesRecursivelyByCanonicalPath(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	finance, err := s.Mkdir(ctx, s.RootID(), "finance")
	require.NoError(t, err)
	archive, err := s.Mkdir(ctx, finance.ID, "archive")
	require.NoError(t, err)
	report, err := s.CreateFile(ctx, finance.ID, "report.txt", fakeHash("71"), 7, "text/plain")
	require.NoError(t, err)
	old, err := s.CreateFile(ctx, archive.ID, "old.pdf", fakeHash("72"), 8, "application/pdf")
	require.NoError(t, err)
	_, err = s.CreateFile(ctx, s.RootID(), "root.txt", fakeHash("73"), 9, "text/plain")
	require.NoError(t, err)
	trashed, err := s.CreateFile(ctx, finance.ID, "trashed.txt", fakeHash("74"), 10, "text/plain")
	require.NoError(t, err)
	_, _, err = s.Trash(ctx, trashed.ID, trashed.Revision)
	require.NoError(t, err)

	first, err := s.DocumentPage(ctx, 0, 2, 0)
	require.NoError(t, err)
	assert.Equal(t, s.RootID(), first.Directory.Node.ID)
	assert.Equal(t, "/", first.Directory.Path)
	assert.Equal(t, 3, first.Total)
	require.Len(t, first.Documents, 2)
	assert.Equal(t, []string{"/finance/archive/old.pdf", "/finance/report.txt"},
		[]string{first.Documents[0].Path, first.Documents[1].Path})
	assert.Equal(t, old.CurrentVersionID, first.Documents[0].Node.CurrentVersionID)

	last, err := s.DocumentPage(ctx, 0, 2, 2)
	require.NoError(t, err)
	require.Len(t, last.Documents, 1)
	assert.Equal(t, "/root.txt", last.Documents[0].Path)

	scoped, err := s.DocumentPage(ctx, finance.ID, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, finance.ID, scoped.Directory.Node.ID)
	assert.Equal(t, "/finance", scoped.Directory.Path)
	assert.Equal(t, 2, scoped.Total)
	require.Len(t, scoped.Documents, 2)
	assert.Equal(t, report.ID, scoped.Documents[1].Node.ID)

	_, err = s.DocumentPage(ctx, report.ID, 10, 0)
	require.ErrorIs(t, err, ErrNotDir)
	_, err = s.DocumentPage(ctx, 1<<62, 10, 0)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.DocumentPage(ctx, finance.ID, 0, 0)
	require.ErrorContains(t, err, "document page limit")
	_, err = s.DocumentPage(ctx, finance.ID, 10, -1)
	require.ErrorContains(t, err, "document page offset")
}
