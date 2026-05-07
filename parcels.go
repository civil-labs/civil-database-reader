package main

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	parcelsv1 "github.com/civil-labs/civil-api-go/civil/mesh/parcels/v1"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ParcelServer struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

func (s *ParcelServer) GetParcelsById(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelsByIdRequest],
) (*connect.Response[parcelsv1.GetParcelsByIdResponse], error) {

	s.logger.Debug("received GetParcelsById request")

	parcels := make(map[string]*parcelsv1.Parcel, 3)

	neighborhood := "dcf2972a-2278-4be0-a756-22d1a48e7171"
	marketArea := "1e481f56-d464-408e-8ef9-63dd58b29eff"
	far := 0.5
	minLotSize := 5000.0
	maxHeight := 20.0
	totalArea := 2000.0
	bath := int32(3)
	bed := int32(4)
	yearBuilt := int32(1963)

	parcels["77f0f90d-2b30-4b84-a63b-01354d64179e"] = &parcelsv1.Parcel{
		ParcelId:                 "77f0f90d-2b30-4b84-a63b-01354d64179e",
		Address:                  "123 Prophet Way, San Francisco, CA, 94102",
		AddressId:                "3d63f781-f551-4a44-b396-078dd6a69230",
		OwnerName:                "Henry George",
		OwnerAddress:             "413 S. 10th Street, Philadelphia, PA, 19147",
		OwnerId:                  "dcf2972a-2278-4be0-a756-22d1a48e7171",
		LandAreaSqFt:             42341.123,
		LandUseId:                "Residential Single-Family",
		NeighborhoodId:           &neighborhood,
		MarketAreaId:             &marketArea,
		ZoningIds:                []string{"edfb09ed-7dd3-4c43-8e62-057132676c28", "aa9fc8f4-c646-41df-ba3b-5ffd673ad60a"},
		MarketLandValue:          "1132234.92",
		AssessedLandValue:        "9032234.92",
		MarketImprovementValue:   "450000",
		AssessedImprovementValue: "450000",

		Affordances: &parcelsv1.ParcelAffordances{
			AffordanceIds:  []string{"69faa787-88e8-4b61-a67d-e23e82e903df", "e6a204ed-fa96-4456-83f7-0a809f5362f8"},
			MaxFar:         &far,
			MinLotSizeSqFt: &minLotSize,
			MaxHeightFt:    &maxHeight,
		},

		ImprovementSummary: &parcelsv1.ParcelImprovementsSummary{
			ImprovementIds:  []string{"6a4aaaea-f96e-430b-a646-963a88856e25", "f349b4b1-c7e3-4a88-96e0-e8fb297384ed"},
			TotalAreaSqFt:   &totalArea,
			TotalBathrooms:  &bath,
			TotalBedrooms:   &bed,
			OldestYearBuilt: &yearBuilt,
			NewestYearBuilt: &yearBuilt,
		},

		Properties: "",
	}

	res := &parcelsv1.GetParcelsByIdResponse{
		Parcels: parcels,
	}

	return connect.NewResponse(res), nil

	// // Strip hidden newlines/spaces and force lowercase so sanitize result will matche Postgres's default behavior
	// cleanAttr := strings.TrimSpace(req.Msg.GetAttributeName())
	// cleanAttr = strings.ToLower(cleanAttr)

	// safeColumn := pgx.Identifier{cleanAttr}.Sanitize()

	// // Safely inject the sanitized identifier into the query string
	// query := fmt.Sprintf(`SELECT %s::text FROM parcels WHERE parcel_id = $1`, safeColumn)

	// s.logger.Debug("executing database query", slog.String("query", query))

	// var value *string
	// err := s.db.QueryRow(ctx, query, req.Msg.GetParcelId()).Scan(&value)
	// if err != nil {
	// 	// Gracefully handle the "column does not exist" error

	// 	s.logger.Debug("GetParcelAttribute query failed", slog.Any("error", err))

	// 	var pgErr *pgconn.PgError
	// 	if errors.As(err, &pgErr) && pgErr.Code == "42703" { // 42703 is the Postgres code for undefined_column
	// 		msg := fmt.Sprintf("attribute %s does not exist", req.Msg.AttributeName)
	// 		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	// 	}

	// 	if errors.Is(err, pgx.ErrNoRows) {
	// 		return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
	// 	}

	// 	return nil, connect.NewError(connect.CodeInternal, errors.New("failed to retrieve attribute"))
	// }

	// res := &parcelsv1.GetParcelAttributeResponse{
	// 	AttributeValue: *value,
	// }
	// return connect.NewResponse(res), nil
}

func (s *ParcelServer) UpdateParcel(
	ctx context.Context,
	req *connect.Request[parcelsv1.UpdateParcelRequest],
) (*connect.Response[parcelsv1.UpdateParcelResponse], error) {

	// if req.Msg.ParcelId == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	// }
	// if req.Msg.PropertyName == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("property name is required"))
	// }

	// query := `SELECT properties ->> $2 FROM parcels WHERE id = $1`

	// // Execute Query
	// var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	// err := s.db.QueryRow(ctx, query, req.Msg.ParcelId, req.Msg.PropertyName).Scan(&value)

	// if err != nil {
	// 	if errors.Is(err, pgx.ErrNoRows) {
	// 		return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
	// 	}
	// 	return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	// }

	// if value == nil {
	// 	msg := fmt.Sprintf("property %s not found for parcel %s", req.Msg.PropertyName, req.Msg.ParcelId)
	// 	return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	// }

	res := &parcelsv1.UpdateParcelResponse{}
	return connect.NewResponse(res), nil
}

func (s *ParcelServer) GetCategoricalParcelStatsById(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetCategoricalParcelStatsByIdRequest],
) (*connect.Response[parcelsv1.GetCategoricalParcelStatsByIdResponse], error) {

	// if req.Msg.ParcelId == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	// }

	// query := `SELECT * FROM parcels WHERE id = $1`

	// // Execute Query
	// var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	// err := s.db.QueryRow(ctx, query, req.Msg.ParcelId).Scan(&value)

	// if err != nil {
	// 	if errors.Is(err, pgx.ErrNoRows) {
	// 		return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
	// 	}
	// 	return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	// }

	// if value == nil {
	// 	msg := fmt.Sprintf("parcel %s not found", req.Msg.ParcelId)
	// 	return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	// }

	res := &parcelsv1.GetCategoricalParcelStatsByIdResponse{}

	return connect.NewResponse(res), nil
}

func (s *ParcelServer) GetNumericalParcelStatsById(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetNumericalParcelStatsByIdRequest],
) (*connect.Response[parcelsv1.GetNumericalParcelStatsByIdResponse], error) {

	// if req.Msg.ParcelId == "" {
	// 	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	// }

	// query := `SELECT * FROM parcels WHERE id = $1`

	// // Execute Query
	// var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	// err := s.db.QueryRow(ctx, query, req.Msg.ParcelId).Scan(&value)

	// if err != nil {
	// 	if errors.Is(err, pgx.ErrNoRows) {
	// 		return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
	// 	}
	// 	return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	// }

	// if value == nil {
	// 	msg := fmt.Sprintf("parcel %s not found", req.Msg.ParcelId)
	// 	return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	// }

	res := &parcelsv1.GetNumericalParcelStatsByIdResponse{}

	return connect.NewResponse(res), nil
}
