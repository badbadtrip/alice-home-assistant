// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package pairing

import (
	"github.com/brutella/hc/db"
	"github.com/brutella/hc/hap/pair"
	"github.com/brutella/hc/util"
)

type Controller struct {
	ctrl *pair.PairingController
	db   db.Database
}

func (c Controller) Handle(cont util.Container) (util.Container, error) {
	method := pair.PairMethodType(cont.GetByte(pair.TagPairingMethod))
	if method == 0x5 { // List pairing
		entities, err := c.db.Entities()
		if err != nil {
			return nil, err
		}

		out := util.NewTLV8Container()
		out.SetByte(pair.TagSequence, 0x2)

		for index, entity := range entities {
			out.SetString(pair.TagUsername, entity.Name)
			out.SetBytes(pair.TagPublicKey, entity.PublicKey)
			out.SetByte(pair.TagPermission, 0x01)
			if index != 0 {
				out.SetByte(255, 0)
			}
		}
		return out, nil
	}
	return c.ctrl.Handle(cont)
}

func NewController(database db.Database) *Controller {
	return &Controller{ctrl: pair.NewPairingController(database), db: database}
}
