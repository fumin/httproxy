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
			return -1, errors.Errorf("%d", i)
		}
		if tx.Type != monero.TxTypeIn {
			return -1, errors.Errorf("%d", i)
		}
		if tx.UnlockTime != 0 {
			return -1, errors.Errorf("%d", i)
		}
		if tx.Confirmations < tx.SuggestedConfirmationsThreshold {
			return -1, errors.Errorf("%d", i)
		}
		amount += tx.Amount
	}
	return amount, nil
}
