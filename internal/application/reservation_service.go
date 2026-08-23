package application

import (
	"context"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// ReservationService serves the read-only reservation queries (ISSUE-180:
// list_resource_reservations, get_resource_reservation). Reservation
// mutations are lease-authenticated attempt operations and live on
// AttemptService instead (claim-time acquisition via ClaimIssue,
// reserve_resources, release_resources).
type ReservationService struct {
	repository ports.ReservationRepository
}

// NewReservationService returns a reservation service backed by repository.
func NewReservationService(repository ports.ReservationRepository) (*ReservationService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "reservation repository is required", false)
	}
	return &ReservationService{repository: repository}, nil
}

// ListReservations serves list_resource_reservations: issue, attempt, kind,
// and active-state filtering with cursor pagination across both active and
// released reservations.
func (service *ReservationService) ListReservations(ctx context.Context, input domain.ListResourceReservationsInput) (domain.ReservationList, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.ReservationList{}, err
	}
	return service.repository.ListReservations(ctx, ports.ListReservationsCommand{Input: normalized})
}

// GetReservation loads one reservation, active or released, by id.
func (service *ReservationService) GetReservation(ctx context.Context, id string) (domain.Reservation, error) {
	return service.repository.GetReservation(ctx, id)
}
