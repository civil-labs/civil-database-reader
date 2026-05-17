package main

import (
	"context"
	"fmt"
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

	s.logger.Debug("received GetParcelsById request", slog.Any("parcelIds", req.Msg.ParcelIds))

	parcelIds := req.Msg.ParcelIds

	// Empty arrays are blocked by the connect handler via the proto def

	s.logger.Debug("creating GetParcelsById map")

	parcels := make(map[string]*parcelsv1.Parcel, len(parcelIds))

	s.logger.Debug("building GetParcelsById query")

	query := `
		SELECT 
			p.public_id::text,
			aa.formatted_address,
			a.public_id::text,
			oa.name,
			oada.formatted_address,
			o.public_id::text,
			pa.land_area_sq_m,
			pa.frontage_m,
			pa.depth_m,
			lu.land_use_id,
			pa.properties::text
		FROM parcels p
		LEFT JOIN parcel_attributes pa ON p.parcel_id = pa.parcel_id 
		LEFT JOIN addresses a ON pa.address_id = a.address_id
		LEFT JOIN address_attributes aa ON a.address_id = aa.address_id
		LEFT JOIN owners o ON pa.owner_id = o.owner_id
		LEFT JOIN owner_attributes oa ON o.owner_id = oa.owner_id
		LEFT JOIN addresses oad ON oa.address_id = oad.address_id
		LEFT JOIN address_attributes oada ON oad.address_id = oada.address_id
		LEFT JOIN land_uses lu ON pa.land_use_id = lu.land_use_id
		WHERE p.public_id = ANY($1::uuid[])
	`

	rows, err := s.db.Query(ctx, query, parcelIds)

	s.logger.Debug("performed GetParcelsById query")

	if err != nil {
		s.logger.Error("failed to query parcels", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve parcel data"))
	}
	defer rows.Close()

	for rows.Next() {
		s.logger.Debug("scanning row")

		// 1. Declare pointers for optional fields
		var (
			parcelID     string
			address      *string
			addressID    *string
			ownerName    *string
			ownerAddress *string
			ownerID      *string
			landAreaSqM  *float64
			frontageM    *float64
			depthM       *float64
			landUseID    *string
			properties   *string
		)

		// 2. Scan directly into the pointers
		err := rows.Scan(
			&parcelID,
			&address,
			&addressID,
			&ownerName,
			&ownerAddress,
			&ownerID,
			&landAreaSqM,
			&frontageM,
			&depthM,
			&landUseID,
			&properties,
		)

		s.logger.Debug("scanned row", slog.Any("parcelId", parcelID))

		if err != nil {
			s.logger.Error("failed to scan parcel row", "error", err, "parcelId", parcelID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data unmarshaling error"))
		}

		// 3. Metric to Imperial Conversions (safely handling nils)
		var landAreaSqFt, frontageFt, depthFt *float64

		if landAreaSqM != nil {
			// 1 sq meter = 10.7639 sq feet
			val := *landAreaSqM * 10.7639
			landAreaSqFt = &val
		}
		if frontageM != nil {
			// 1 meter = 3.28084 feet
			val := *frontageM * 3.28084
			frontageFt = &val
		}
		if depthM != nil {
			val := *depthM * 3.28084
			depthFt = &val
		}

		s.logger.Debug("converted units")

		affordances := &parcelsv1.ParcelAffordances{
			AffordanceIds: []string{}, // Guarantees a non-nil slice (JSON "[]")
		}

		improvementSummary := &parcelsv1.ParcelImprovementsSummary{
			ImprovementIds: []string{}, // Guarantees a non-nil slice
		}

		// 4. Populate the Protobuf map
		// Assuming your proto generates pointers (*string, *float64) for optional fields
		parcels[parcelID] = &parcelsv1.Parcel{
			ParcelId:           parcelID,
			Address:            address,
			AddressId:          addressID,
			OwnerName:          ownerName,
			OwnerAddress:       ownerAddress,
			OwnerId:            ownerID,
			LandAreaSqFt:       landAreaSqFt,
			FrontageFt:         frontageFt,
			DepthFt:            depthFt,
			LandUseId:          landUseID,
			Affordances:        affordances,
			ImprovementSummary: improvementSummary,
			Properties:         properties,
		}

		s.logger.Debug("populated protobuf map")
	}

	if err := rows.Err(); err != nil {
		s.logger.Error("error iterating parcel rows", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to process parcel stream"))
	}

	s.logger.Debug("constructing response")

	// Wrap the map in your response payload
	res := &parcelsv1.GetParcelsByIdResponse{
		Parcels: parcels,
	}

	s.logger.Debug("sending response")

	return connect.NewResponse(res), nil

	// rows, err := s.db.Query(ctx, query, parcelIds)

	// parcels["77f0f90d-2b30-4b84-a63b-01354d64179e"] = &parcelsv1.Parcel{
	// 	ParcelId:          "77f0f90d-2b30-4b84-a63b-01354d64179e",
	// 	Address:           "123 Prophet Way, San Francisco, CA, 94102",
	// 	AddressId:         "3d63f781-f551-4a44-b396-078dd6a69230",
	// 	OwnerName:         "Henry George",
	// 	OwnerAddress:      "413 S. 10th Street, Philadelphia, PA, 19147",
	// 	OwnerId:           proto.String("dcf2972a-2278-4be0-a756-22d1a48e7171"),
	// 	LandAreaSqFt:      proto.Float64(42341.123),
	// 	FrontageM:         proto.Float64(4323.1),
	// 	DepthM:            proto.Float64(24.1),
	// 	LandUseId:         "Residential Single-Family",
	// 	NeighborhoodId:    &neighborhood,
	// 	ZoningIds:         []string{"edfb09ed-7dd3-4c43-8e62-057132676c28", "aa9fc8f4-c646-41df-ba3b-5ffd673ad60a"},
	// 	MarketLandValue:   proto.String("1132234.92"),
	// 	AssessedLandValue: proto.String("9032234.92"),

	// 	Affordances: &parcelsv1.ParcelAffordances{
	// 		AffordanceIds:  []string{"69faa787-88e8-4b61-a67d-e23e82e903df", "e6a204ed-fa96-4456-83f7-0a809f5362f8"},
	// 		MaxFar:         &far,
	// 		MinLotSizeSqFt: &minLotSize,
	// 		MaxHeightFt:    &maxHeight,
	// 	},

	// 	ImprovementSummary: &parcelsv1.ParcelImprovementsSummary{
	// 		ImprovementIds:           []string{"6a4aaaea-f96e-430b-a646-963a88856e25", "f349b4b1-c7e3-4a88-96e0-e8fb297384ed"},
	// 		TotalAreaSqFt:            totalArea,
	// 		TotalBathrooms:           bath,
	// 		TotalBedrooms:            bed,
	// 		TotalUnits:               1,
	// 		OldestYearBuilt:          &yearBuilt,
	// 		NewestYearBuilt:          &yearBuilt,
	// 		WorstConditionId:         nil,
	// 		BestConditionId:          nil,
	// 		MarketImprovementValue:   proto.String("450000"),
	// 		AssessedImprovementValue: proto.String("450000"),
	// 	},

	// 	Properties: "",
	// }

	// res := &parcelsv1.GetParcelsByIdResponse{
	// 	Parcels: parcels,
	// }

	// return connect.NewResponse(res), nil

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
