package server

// rbac_test.go — tests for requireGroupAccess middleware (S3-1)
//
// Tests cover:
//   - Admin scope (API key): passes unconditionally.
//   - Operator session: allowed when user has membership in node's group.
//   - Operator session: denied when node has no group.
//   - Operator session: denied when user is not a member of node's group.
//   - Readonly session: always 403.
//   - Node-scoped key: always 403.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// newTestDB opens an in-memory SQLite DB (via a temp file) and registers
// cleanup. It lives in package server so internal-package tests can call it.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// setupRBACTestDB creates a fresh in-memory DB and seeds:
//   - one node group
//   - two nodes (one in the group, one ungrouped)
//   - two users (one admin, one operator)
//   - a user_group_memberships row for the operator → the group
//
// Returns (testDB, adminUserID, operatorUserID, groupID, groupedNodeID, ungroupedNodeID).
func setupRBACTestDB(t *testing.T) (*db.DB, string, string, string, string, string) {
	t.Helper()
	database := newTestDB(t)

	// Create node group.
	groupID := uuid.New().String()
	g := api.NodeGroup{
		ID:   groupID,
		Name: "compute",
	}
	if err := database.CreateNodeGroupFull(context.Background(), g); err != nil {
		t.Fatalf("create node group: %v", err)
	}

	// Create a base image (FK requirement for nodes).
	imgID := uuid.New().String()
	img := api.BaseImage{ID: imgID, Name: "test-img", Format: "filesystem", Status: "ready"}
	if err := database.CreateBaseImage(context.Background(), img); err != nil {
		t.Fatalf("create base image: %v", err)
	}

	// Create grouped node.
	groupedNodeID := uuid.New().String()
	if err := database.CreateNodeConfig(context.Background(), api.NodeConfig{
		ID:          groupedNodeID,
		Hostname:    "node-01",
		PrimaryMAC:  "aa:bb:cc:dd:ee:01",
		BaseImageID: imgID,
		GroupID:     groupID,
	}); err != nil {
		t.Fatalf("create grouped node: %v", err)
	}

	// Create ungrouped node.
	ungroupedNodeID := uuid.New().String()
	if err := database.CreateNodeConfig(context.Background(), api.NodeConfig{
		ID:          ungroupedNodeID,
		Hostname:    "node-02",
		PrimaryMAC:  "aa:bb:cc:dd:ee:02",
		BaseImageID: imgID,
	}); err != nil {
		t.Fatalf("create ungrouped node: %v", err)
	}

	// Create operator user.
	operatorUserID := uuid.New().String()
	if err := database.CreateUser(context.Background(), db.UserRecord{
		ID:           operatorUserID,
		Username:     "operator1",
		PasswordHash: "$2a$12$placeholder",
		Role:         db.UserRoleOperator,
	}); err != nil {
		t.Fatalf("create operator user: %v", err)
	}

	// Assign operator to the group.
	if err := database.SetUserGroupMemberships(context.Background(), operatorUserID, []string{groupID}); err != nil {
		t.Fatalf("set group memberships: %v", err)
	}

	// Admin user ID (just a UUID — doesn't need to be in DB for middleware test
	// since admin bypasses the membership check).
	adminUserID := uuid.New().String()

	return database, adminUserID, operatorUserID, groupID, groupedNodeID, ungroupedNodeID
}

// buildRBACRequest builds a chi request with a URL param set to nodeID
// and the given scope/userID/userRole in context.
func buildRBACRequest(nodeID, scope, userID, userRole string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/reimage", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", nodeID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, ctxKeyScope{}, api.KeyScope(scope))
	if userID != "" {
		ctx = context.WithValue(ctx, ctxKeyUserID{}, userID)
	}
	if userRole != "" {
		ctx = context.WithValue(ctx, ctxKeyUserRole{}, userRole)
	}
	return req.WithContext(ctx)
}

func TestRequireGroupAccess_AdminPasses(t *testing.T) {
	database, adminUserID, _, _, groupedNodeID, _ := setupRBACTestDB(t)

	mw := requireGroupAccess("id", database)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := buildRBACRequest(groupedNodeID, string(api.KeyScopeAdmin), adminUserID, "admin")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("admin: expected 200, got %d", rr.Code)
	}
}

func TestRequireGroupAccess_OperatorInGroupPasses(t *testing.T) {
	database, _, operatorUserID, _, groupedNodeID, _ := setupRBACTestDB(t)

	mw := requireGroupAccess("id", database)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := buildRBACRequest(groupedNodeID, string(api.KeyScopeOperator), operatorUserID, "operator")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("operator in group: expected 200, got %d", rr.Code)
	}
}

func TestRequireGroupAccess_OperatorNotInGroupDenied(t *testing.T) {
	database, _, operatorUserID, _, _, _ := setupRBACTestDB(t)

	// Create a second group that operator is NOT a member of.
	otherGroupID := uuid.New().String()
	if err := database.CreateNodeGroupFull(context.Background(), api.NodeGroup{
		ID: otherGroupID, Name: "storage",
	}); err != nil {
		t.Fatalf("create other group: %v", err)
	}

	// Create a node in the other group.
	imgID := uuid.New().String()
	_ = database.CreateBaseImage(context.Background(), api.BaseImage{ID: imgID, Name: "img2", Format: "filesystem", Status: "ready"})
	otherNodeID := uuid.New().String()
	if err := database.CreateNodeConfig(context.Background(), api.NodeConfig{
		ID: otherNodeID, Hostname: "node-03", PrimaryMAC: "aa:bb:cc:dd:ee:03",
		BaseImageID: imgID, GroupID: otherGroupID,
	}); err != nil {
		t.Fatalf("create other node: %v", err)
	}

	mw := requireGroupAccess("id", database)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := buildRBACRequest(otherNodeID, string(api.KeyScopeOperator), operatorUserID, "operator")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("operator not in group: expected 403, got %d", rr.Code)
	}
}

func TestRequireGroupAccess_OperatorUngroupedNodeDenied(t *testing.T) {
	database, _, operatorUserID, _, _, ungroupedNodeID := setupRBACTestDB(t)

	mw := requireGroupAccess("id", database)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := buildRBACRequest(ungroupedNodeID, string(api.KeyScopeOperator), operatorUserID, "operator")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("operator on ungrouped node: expected 403, got %d", rr.Code)
	}
}

func TestRequireGroupAccess_ReadonlyDenied(t *testing.T) {
	database, _, _, _, groupedNodeID, _ := setupRBACTestDB(t)

	mw := requireGroupAccess("id", database)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	readonlyUserID := uuid.New().String()
	req := buildRBACRequest(groupedNodeID, "readonly", readonlyUserID, "readonly")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("readonly: expected 403, got %d", rr.Code)
	}
}

func TestRequireGroupAccess_NodeScopeDenied(t *testing.T) {
	database, _, _, _, groupedNodeID, _ := setupRBACTestDB(t)

	mw := requireGroupAccess("id", database)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := buildRBACRequest(groupedNodeID, string(api.KeyScopeNode), "", "")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("node scope: expected 403, got %d", rr.Code)
	}
}

func TestRequireGroupAccess_UnauthenticatedDenied(t *testing.T) {
	database, _, _, _, groupedNodeID, _ := setupRBACTestDB(t)

	mw := requireGroupAccess("id", database)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No scope in context.
	req := buildRBACRequest(groupedNodeID, "", "", "")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: expected 401, got %d", rr.Code)
	}
}
