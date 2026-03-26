package nextstore

import "github.com/cosmo-local-credit/clc-core/internal/store"

type ERC20Workflow = store.NextERC20Workflow
type ERC20WorkflowUpdate = store.NextERC20WorkflowUpdate

const (
	ERC20ActionDeploy  = store.NextERC20ActionDeploy
	ERC20ActionUpgrade = store.NextERC20ActionUpgrade

	ERC20StatusPending = store.NextERC20StatusPending
	ERC20StatusRunning = store.NextERC20StatusRunning
	ERC20StatusSuccess = store.NextERC20StatusSuccess
	ERC20StatusFailed  = store.NextERC20StatusFailed
)
