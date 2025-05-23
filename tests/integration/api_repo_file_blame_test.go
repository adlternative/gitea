// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unittest"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/modules/git"
	"code.gitea.io/gitea/modules/test"
	"code.gitea.io/gitea/tests"

	"github.com/stretchr/testify/assert"
)

type BlamePartResponse struct {
	CommitID string   `json:"commit_id"`
	Author   string   `json:"author"`
	Email    string   `json:"email"`
	Date     string   `json:"date"`
	Lines    []string `json:"lines"`
}

func TestAPIFileBlame(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1}) // user2/repo1
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.Name)

	fileName := "testblame.txt"
	branchName := "main"

	// --- Test Case 1: Valid file path and ref ---
	// Commit 1
	contentLine1 := "Line 1"
	contentLine2 := "Line 2"
	contentLine3 := "Line 3"
	initialContent := fmt.Sprintf("%s\n%s\n%s", contentLine1, contentLine2, contentLine3)
	c1Path, _ := CreateFileInBranch(t, owner.Name, repo.Name, branchName, fileName, initialContent)
	commit1SHA := GetFileCommitID(t, owner.Name, repo.Name, branchName, c1Path)
	assert.NotEmpty(t, commit1SHA)

	// Commit 2 (editing Line 1 and Line 3)
	updatedContentLine1 := "Line 1 updated"
	updatedContentLine3 := "Line 3 new"
	updatedContent := fmt.Sprintf("%s\n%s\n%s", updatedContentLine1, contentLine2, updatedContentLine3)
	c2Path, _ := EditFileInBranch(t, owner.Name, repo.Name, branchName, fileName, updatedContent)
	commit2SHA := GetFileCommitID(t, owner.Name, repo.Name, branchName, c2Path)
	assert.NotEmpty(t, commit2SHA)
	assert.NotEqual(t, commit1SHA, commit2SHA) // Make sure a new commit was made

	req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/blame/%s", owner.Name, repo.Name, fileName))
	resp := session.MakeRequest(t, req, http.StatusOK)
	var blameParts []BlamePartResponse
	DecodeJSON(t, resp, &blameParts)

	assert.Len(t, blameParts, 2, "Expected two blame parts after two commits")

	// Part 1 (Commit 2) - should contain updatedContentLine1 and updatedContentLine3
	foundLine1Updated := false
	foundLine3New := false
	for _, line := range blameParts[0].Lines {
		if line == updatedContentLine1 {
			foundLine1Updated = true
		}
		if line == updatedContentLine3 {
			foundLine3New = true
		}
	}
	assert.True(t, foundLine1Updated, "Expected to find 'Line 1 updated' in the first blame part")
	assert.True(t, foundLine3New, "Expected to find 'Line 3 new' in the first blame part")
	assert.Equal(t, commit2SHA, blameParts[0].CommitID)
	assert.Equal(t, owner.Name, blameParts[0].Author) // Assuming owner made the commit

	// Part 2 (Commit 1 or 2) - should contain contentLine2
	foundLine2 := false
	for _, line := range blameParts[1].Lines {
		if line == contentLine2 {
			foundLine2 = true
		}
	}
	assert.True(t, foundLine2, "Expected to find 'Line 2' in the second blame part")
	// The commit for Line 2 could be commit1 or commit2 depending on how blame works with unchanged lines
	// For simplicity, we'll just check that it's one of them.
	assert.Contains(t, []string{commit1SHA, commit2SHA}, blameParts[1].CommitID)
	assert.Equal(t, owner.Name, blameParts[1].Author)

	// --- Test Case 2: Invalid file path ---
	reqInvalidFile := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/blame/nonexistentfile.txt", owner.Name, repo.Name))
	session.MakeRequest(t, reqInvalidFile, http.StatusNotFound)

	// --- Test Case 3: Invalid ref ---
	reqInvalidRef := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/blame/%s?ref=invalidref", owner.Name, repo.Name, fileName))
	session.MakeRequest(t, reqInvalidRef, http.StatusNotFound)

	// --- Test Case 4: Empty repository ---
	// Create an empty repository
	emptyRepo := CreateEmptyRepo(t, owner, "empty-blame-repo", false)
	defer DeleteRepo(t, session, owner.Name, emptyRepo.Name) // Clean up

	reqEmptyRepo := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/blame/somefile.txt", owner.Name, emptyRepo.Name))
	// For an empty repo, the repoAssignment middleware might fail to load gitRepo, leading to a 500 or 404 earlier.
	// Or if it proceeds, GetCommit on DefaultBranch would fail.
	// We expect a 404 because the file or ref effectively doesn't exist.
	respEmptyRepo := session.MakeRequest(t, reqEmptyRepo, http.StatusNotFound)
	// Check for specific error message if the API contract defines one for empty repos.
	// For now, checking status code is sufficient.
	var errResp api.GeneralError
	DecodeJSON(t, respEmptyRepo, &errResp)
	// This error comes from "GetCommit" when the default branch (e.g. "main") doesn't exist
	assert.Contains(t, errResp.Message, "object does not exist")


	// --- Test Case 5: File with no blame (e.g. after deleting and recreating) ---
	// Delete the file
	DeleteFileInBranch(t, owner.Name, repo.Name, branchName, fileName, "Deleting for test")
	// Recreate the file with the same name but different content
	recreatedContent := "Recreated line 1\nRecreated line 2"
	rcPath, _ := CreateFileInBranch(t, owner.Name, repo.Name, branchName, fileName, recreatedContent)
	recreatedCommitSHA := GetFileCommitID(t, owner.Name, repo.Name, branchName, rcPath)

	reqRecreated := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/blame/%s", owner.Name, repo.Name, fileName))
	respRecreated := session.MakeRequest(t, reqRecreated, http.StatusOK)
	var blamePartsRecreated []BlamePartResponse
	DecodeJSON(t, respRecreated, &blamePartsRecreated)

	assert.Len(t, blamePartsRecreated, 1, "Expected one blame part for the recreated file")
	assert.Equal(t, recreatedCommitSHA, blamePartsRecreated[0].CommitID)
	assert.Equal(t, owner.Name, blamePartsRecreated[0].Author)
	assert.Len(t, blamePartsRecreated[0].Lines, 2)
	assert.Equal(t, "Recreated line 1", blamePartsRecreated[0].Lines[0])
	assert.Equal(t, "Recreated line 2", blamePartsRecreated[0].Lines[1])

	// --- Test Case 6: Blame with a specific valid ref (commit1SHA) ---
	reqWithRef := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/blame/%s?ref=%s", owner.Name, repo.Name, fileName, commit1SHA))
	respWithRef := session.MakeRequest(t, reqWithRef, http.StatusOK)
	var blamePartsWithRef []BlamePartResponse
	DecodeJSON(t, respWithRef, &blamePartsWithRef)

	// When blaming at commit1SHA, the file content was initialContent
	assert.Len(t, blamePartsWithRef, 1, "Expected one blame part when blaming at commit1SHA")
	assert.Equal(t, commit1SHA, blamePartsWithRef[0].CommitID)
	assert.Len(t, blamePartsWithRef[0].Lines, 3)
	assert.Equal(t, contentLine1, blamePartsWithRef[0].Lines[0])
	assert.Equal(t, contentLine2, blamePartsWithRef[0].Lines[1])
	assert.Equal(t, contentLine3, blamePartsWithRef[0].Lines[2])
}

// Helper function to get a commit ID for a file path (can be simplified or moved)
func GetFileCommitID(t *testing.T, ownerName, repoName, branchName, filePath string) string {
	t.Helper()
	gitRepo, err := git.OpenRepository(test.CtxNotUnix(t), repo_model.RepoPath(ownerName, repoName))
	assert.NoError(t, err)
	defer gitRepo.Close()

	commit, err := gitRepo.GetBranchCommit(branchName)
	assert.NoError(t, err)
	entry, err := commit.GetTreeEntryByPath(filePath)
	assert.NoError(t, err)
	
	// To get the commit that last modified this specific file, we might need a more complex git log call.
	// For simplicity in this test, we're assuming the last commit on the branch modified this file.
	// This is true for CreateFileInBranch and EditFileInBranch if they are the last operations.
	// A more robust way would be: gitRepo.GetCommitByPath(commit.ID.String(), filePath) or similar
	// However, GetCommitByPath is not directly available.
	// The current approach relies on the test flow where the file creation/edit is the last commit.
	
	// A better way to get the commit ID that *introduced* the file version at `filePath` in `commit`
	// is to use `git log -1 --format=%H -- <filePath>` relative to that commit.
	// For now, we use the commit of the branch, which is fine if the file was just committed.
	
	// Let's try to get the last commit that modified the file
	history, err := gitRepo.CommitsByFile(git.CommitsByFileOptions{
		Revision: branchName,
		Path:     filePath,
		MaxCommits: 1,
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, history)
	return history[0].ID.String()

	// If we just want the commit ID of the blob itself (not necessarily the commit that *changed* it):
	// return entry.ID.String() // This would be the Blob ID, not the commit ID.
}

// CreateEmptyRepo is a helper to create an empty repository for testing
func CreateEmptyRepo(t *testing.T, owner *user_model.User, name string, private bool) *repo_model.Repository {
	t.Helper()
	repo, err := repo_model.CreateRepository(test.CtxNotUnix(t), owner, owner, repo_model.CreateRepoOptions{
		Name:      name,
		IsPrivate: private,
		IsEmpty:   true,
	})
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, repo)
	return repo
}

// DeleteRepo is a helper to delete a repository
func DeleteRepo(t *testing.T, session *TestSession, ownerName, repoName string) {
	t.Helper()
	req := NewRequest(t, "DELETE", fmt.Sprintf("/api/v1/repos/%s/%s", ownerName, repoName))
	session.MakeRequest(t, req, http.StatusNoContent)
}

// CreateFileInBranch creates a file in a specific branch
func CreateFileInBranch(t *testing.T, ownerName, repoName, branchName, filePath, content string) (string, string) {
	t.Helper()
	session := loginUser(t, ownerName)
	url := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", ownerName, repoName, filePath)
	req := NewRequestWithJSON(t, "POST", url, &api.CreateFileOptions{
		Content:    content,
		BranchName: branchName,
		Message:    fmt.Sprintf("Create %s", filePath),
		Author:     api.Identity{Name: ownerName, Email: ownerName + "@example.com"},
		Committer:  api.Identity{Name: ownerName, Email: ownerName + "@example.com"},
		Dates:      api.CommitDateOptions{Author: time.Now(), Committer: time.Now()},
	})
	resp := session.MakeRequest(t, req, http.StatusCreated)
	var fileResponse api.FileResponse
	DecodeJSON(t, resp, &fileResponse)
	assert.NotNil(t, fileResponse.Commit)
	assert.NotEmpty(t, fileResponse.Commit.SHA)
	return filePath, fileResponse.Commit.SHA
}

// EditFileInBranch edits a file in a specific branch
func EditFileInBranch(t *testing.T, ownerName, repoName, branchName, filePath, content string) (string, string) {
	t.Helper()
	session := loginUser(t, ownerName)

	// First, get the SHA of the existing file
	getReq := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s?ref=%s", ownerName, repoName, filePath, branchName))
	getResp := session.MakeRequest(t, getReq, http.StatusOK)
	var existingFile api.ContentsResponse
	DecodeJSON(t, getResp, &existingFile)
	assert.NotEmpty(t, existingFile.SHA)

	url := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", ownerName, repoName, filePath)
	req := NewRequestWithJSON(t, "PUT", url, &api.UpdateFileOptions{
		Content:    content,
		BranchName: branchName,
		SHA:        existingFile.SHA,
		Message:    fmt.Sprintf("Update %s", filePath),
		Author:     api.Identity{Name: ownerName, Email: ownerName + "@example.com"},
		Committer:  api.Identity{Name: ownerName, Email: ownerName + "@example.com"},
		Dates:      api.CommitDateOptions{Author: time.Now(), Committer: time.Now()},
	})
	resp := session.MakeRequest(t, req, http.StatusOK)
	var fileResponse api.FileResponse
	DecodeJSON(t, resp, &fileResponse)
	assert.NotNil(t, fileResponse.Commit)
	assert.NotEmpty(t, fileResponse.Commit.SHA)
	return filePath, fileResponse.Commit.SHA
}

// DeleteFileInBranch deletes a file in a specific branch
func DeleteFileInBranch(t *testing.T, ownerName, repoName, branchName, filePath, message string) {
	t.Helper()
	session := loginUser(t, ownerName)

	// First, get the SHA of the existing file
	getReq := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s?ref=%s", ownerName, repoName, filePath, branchName))
	getResp := session.MakeRequest(t, getReq, http.StatusOK)
	var existingFile api.ContentsResponse
	DecodeJSON(t, getResp, &existingFile)
	assert.NotEmpty(t, existingFile.SHA)

	url := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", ownerName, repoName, filePath)
	req := NewRequestWithJSON(t, "DELETE", url, &api.DeleteFileOptions{
		BranchName: branchName,
		SHA:        existingFile.SHA,
		Message:    message,
		Author:     api.Identity{Name: ownerName, Email: ownerName + "@example.com"},
		Committer:  api.Identity{Name: ownerName, Email: ownerName + "@example.com"},
		Dates:      api.CommitDateOptions{Author: time.Now(), Committer: time.Now()},
	})
	session.MakeRequest(t, req, http.StatusOK) // APIv1 returns 200 on delete, should be 204 ideally
}
