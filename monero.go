package httproxy

import (
	"github.com/fumin/httproxy/util/monero"
	"github.com/pkg/errors"
)

func xmrReceived(w *monero.Wallet, addr, txid string) (monero.Piconero, error) {
	out, err := w.GetTransferByTxid(monero.GetTransferByTxidIn{Txid: txid})
	if err != nil {
		return -1, errors.Wrap(err, "")
	}
	var amount monero.Piconero
	for i, tx := range out.Transfers {
		if tx.Address != addr {
			return -1, errors.Errorf("address got %s want %s at %d tx", tx.Address, addr, i)
		}
		if tx.Type != monero.TxTypeIn {
			return -1, errors.Errorf("tx type got %s want %s at %d tx", tx.Type, monero.TxTypeIn, i)
		}
		if tx.UnlockTime != 0 {
			return -1, errors.Errorf("unlock_time got %d want %d at %d tx", tx.UnlockTime, 0, i)
		}
		if tx.Confirmations < tx.SuggestedConfirmationsThreshold {
			return -1, errors.Errorf("confirmations got %d want %d %d tx", tx.Confirmations, tx.SuggestedConfirmationsThreshold, i)
		}
		amount += tx.Amount
	}
	return amount, nil
}
