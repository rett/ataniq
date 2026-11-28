package pack

import (
	"context"
	"inventory.test/internal/agentsession"
)

type Settings interface {
	Adjust(context.Context, agentsession.SessionRef, bool)
}
