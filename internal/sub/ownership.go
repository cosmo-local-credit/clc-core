package sub

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
)

func (s *Sub) projectCatalogOwnership(ctx context.Context, tx pgx.Tx, evt event.Event) error {
	if !evt.Success || evt.TxType != "OWNERSHIP_TRANSFERRED" {
		return nil
	}
	owner, ok := payloadAddress(evt, "newOwner")
	if !ok || !common.IsHexAddress(evt.ContractAddress) {
		return fmt.Errorf("malformed ownership event for %s", evt.TxHash)
	}
	_, err := s.store.ProjectCatalogOwnership(ctx, tx, s.chainID, evt.ContractAddress, owner, evt.Block, uint64(evt.Index))
	return err
}
