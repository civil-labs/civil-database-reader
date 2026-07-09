package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	parcelsv1 "github.com/civil-labs/civil-api-go/civil/mesh/parcels/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *APIServer) GetParcelsWithImprovementSummaryByParcelId(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelsWithImprovementSummaryByParcelIdRequest],
) (*connect.Response[parcelsv1.GetParcelsWithImprovementSummaryByParcelIdResponse], error) {

	s.logger.Debug("received GetParcelsWithImprovementSummaryByParcelId request",
		slog.Any("parcel_ids", req.Msg.ParcelIds),
		slog.Any("legal_as_of", req.Msg.GetLegalAsOf()),
		slog.String("valuation_id", req.Msg.GetValuationId()),
		slog.String("neighborhood_definition_id", req.Msg.GetNeighborhoodDefinitionId()),
	)

	parcels, err := s.getParcelsWithImprovementSummary(
		ctx,
		req.Msg.ParcelIds,
		nil,
		req.Msg.GetLegalAsOf(),
		req.Msg.GetValuationId(),
		req.Msg.GetNeighborhoodDefinitionId(),
	)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&parcelsv1.GetParcelsWithImprovementSummaryByParcelIdResponse{
		Parcels: parcels,
	}), nil
}

func (s *APIServer) GetParcelsWithImprovementSummaryByFeatureId(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelsWithImprovementSummaryByFeatureIdRequest],
) (*connect.Response[parcelsv1.GetParcelsWithImprovementSummaryByFeatureIdResponse], error) {

	s.logger.Debug("received GetParcelsWithImprovementSummaryByParcelId request",
		slog.Any("feature_ids", req.Msg.FeatureIds),
		slog.Any("legal_as_of", req.Msg.GetLegalAsOf()),
		slog.String("valuation_id", req.Msg.GetValuationId()),
		slog.String("neighborhood_definition_id", req.Msg.GetNeighborhoodDefinitionId()),
	)

	parcels, err := s.getParcelsWithImprovementSummary(
		ctx,
		nil,
		req.Msg.FeatureIds,
		req.Msg.GetLegalAsOf(),
		req.Msg.GetValuationId(),
		req.Msg.GetNeighborhoodDefinitionId(),
	)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&parcelsv1.GetParcelsWithImprovementSummaryByFeatureIdResponse{
		Parcels: parcels,
	}), nil
}

func (s *APIServer) getParcelsWithImprovementSummary(
	ctx context.Context,
	parcelUUIDs []string,
	featureIDs []int64,
	legalAsOf *timestamppb.Timestamp,
	valuationId string,
	neighborhoodDefinitionId string,
) (map[string]*parcelsv1.ParcelWithImprovementSummary, error) {

	s.logger.Debug("getParcelsWithImprovementSummary called",
		slog.Int("parcelUUIDsCount", len(parcelUUIDs)),
		slog.Int("featureIDsCount", len(featureIDs)),
		slog.String("valuationId", valuationId),
		slog.String("neighborhoodDefinitionId", neighborhoodDefinitionId),
	)

	var targetTime time.Time
	if legalAsOf != nil {
		targetTime = legalAsOf.AsTime()
	} else {
		targetTime = time.Now()
	}

	var valuationIDArg *string
	if valuationId != "" {
		valuationIDArg = &valuationId
	}

	var neighborhoodDefIDArg *string
	if neighborhoodDefinitionId != "" {
		neighborhoodDefIDArg = &neighborhoodDefinitionId
	}

	var filterClause string
	var idsArg any
	if len(parcelUUIDs) > 0 {
		filterClause = "p.public_id = ANY($4::uuid[])"
		idsArg = parcelUUIDs
	} else if len(featureIDs) > 0 {
		filterClause = "pg.feature_id = ANY($4::bigint[])"
		idsArg = featureIDs
	} else {
		return make(map[string]*parcelsv1.ParcelWithImprovementSummary), nil
	}

	query := fmt.Sprintf(`
		WITH matched_parcels AS (
			SELECT p.parcel_id, p.public_id, pg.feature_id
			FROM public.parcels p
			JOIN public.parcel_geometry pg ON p.parcel_id = pg.parcel_id AND pg.legal_valid_range @> $1::timestamptz
			JOIN public.parcel_attributes pa ON p.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
			WHERE %s
		),
		primary_imps AS (
			SELECT * FROM public.get_primary_improvements(ARRAY(SELECT parcel_id FROM matched_parcels), $1::timestamptz)
		),
		imp_totals AS (
			SELECT 
				pi.parcel_id,
				array_remove(array_agg(DISTINCT imp.public_id::text), NULL) AS improvement_ids,
				COALESCE(SUM(attr.area_sq_m), 0) AS total_area_sq_m,
				COALESCE(SUM(attr.bathrooms), 0) AS total_bathrooms,
				COALESCE(SUM(attr.bedrooms), 0) AS total_bedrooms,
				COALESCE(SUM(attr.units), 0) AS total_units,
				SUM(val.market_value)::numeric(19,4)::text AS total_market_improvement_value,
				SUM(val.assessed_value)::numeric(19,4)::text AS total_assessed_improvement_value
			FROM public.parcel_improvements pi
			JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
			LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id 
				AND attr.legal_valid_range @> $1::timestamptz
			LEFT JOIN public.improvement_valuations val ON imp.improvement_id = val.improvement_id
				AND $2::uuid IS NOT NULL
				AND val.valuation_id = (
					SELECT v.valuation_id 
					FROM public.valuations v 
					WHERE v.public_id = $2::uuid
				)
			WHERE pi.parcel_id = ANY(ARRAY(SELECT parcel_id FROM matched_parcels))
				AND pi.legal_valid_range @> $1::timestamptz
				AND imp.is_voided = false
			GROUP BY pi.parcel_id
		),
		primary_imp_details AS (
			SELECT 
				pimp.parcel_id,
				pimp.improvement_id,
				imp.public_id::text AS primary_improvement_public_id,
				attr.year_built AS primary_year_built,
				attr.effective_year_built AS primary_effective_year_built,
				cond.public_id::text AS primary_condition_id
			FROM primary_imps pimp
			JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
			LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id 
				AND attr.legal_valid_range @> $1::timestamptz
			LEFT JOIN public.improvement_conditions cond ON attr.improvement_condition_id = cond.improvement_condition_id 
				AND cond.is_voided = false
			WHERE imp.is_voided = false
		)
		SELECT 
			p.public_id::text,
			LEFT(aa.formatted_address, 1024) AS safe_address,
			a.public_id::text,
			LEFT(parties_agg.primary_owner_name, 128) AS safe_primary_owner_name,
			LEFT(parties_agg.primary_owner_address, 256) AS safe_primary_owner_address,
			parties_agg.party_ids,
			pa.land_area_sq_m,
			pa.frontage_m,
			pa.depth_m,
			lu.public_id::text,
			n.public_id::text,

			aff.zoning_public_ids,
			aff.affordance_ids,
			aff.strict_max_far,
			aff.strict_min_lot_size_sq_m,
			aff.strict_max_height_m,
			aff.strict_max_dwelling_units_per_hectare,
			aff.strict_max_lot_coverage_pct,

			pv_agg.market_land_value,
			pv_agg.assessed_land_value,

			COALESCE(imp_totals.improvement_ids, '{}'::text[]) AS improvement_ids,
			COALESCE(pid.primary_improvement_public_id, '') AS primary_improvement_id,
			COALESCE(imp_totals.total_area_sq_m, 0.0) AS total_area_sq_m,
			COALESCE(imp_totals.total_bathrooms, 0) AS total_bathrooms,
			COALESCE(imp_totals.total_bedrooms, 0) AS total_bedrooms,
			COALESCE(imp_totals.total_units, 0) AS total_units,
			pid.primary_year_built,
			pid.primary_effective_year_built,
			pid.primary_condition_id,
			imp_totals.total_market_improvement_value,
			imp_totals.total_assessed_improvement_value,

			pa.properties::text,
			COALESCE(pg.feature_id, 0)
		FROM matched_parcels mp
		JOIN public.parcels p ON mp.parcel_id = p.parcel_id
		JOIN public.parcel_geometry pg ON p.parcel_id = pg.parcel_id AND pg.legal_valid_range @> $1::timestamptz
		JOIN public.parcel_attributes pa ON p.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		LEFT JOIN public.addresses a ON pa.address_id = a.address_id
		LEFT JOIN public.address_attributes aa ON a.address_id = aa.address_id
		LEFT JOIN public.land_uses lu ON pa.land_use_id = lu.land_use_id

		LEFT JOIN (
			SELECT 
				pp.parcel_id,
				array_agg(pty.public_id::text ORDER BY pp.ownership_share DESC, pty.party_id ASC) AS party_ids,
				(array_agg(pa_attr.name ORDER BY pp.ownership_share DESC, pty.party_id ASC))[1] AS primary_owner_name,
				(array_agg(p_aa.formatted_address ORDER BY pp.ownership_share DESC, pty.party_id ASC))[1] AS primary_owner_address
			FROM public.parcel_parties pp
			JOIN public.parties pty ON pp.party_id = pty.party_id
			LEFT JOIN public.party_attributes pa_attr ON pty.party_id = pa_attr.party_id
			LEFT JOIN public.address_attributes p_aa ON pa_attr.address_id = p_aa.address_id
			WHERE pp.parcel_id = ANY(ARRAY(SELECT parcel_id FROM matched_parcels))
				AND pp.legal_valid_range @> $1::timestamptz
			GROUP BY pp.parcel_id
		) parties_agg ON p.parcel_id = parties_agg.parcel_id

		-- Neighborhood Definition Joins
		LEFT JOIN public.neighborhood_definitions nd 
			ON nd.public_id = $3::uuid
		LEFT JOIN public.parcel_neighborhood_definitions pnd 
			ON p.parcel_id = pnd.parcel_id 
			AND pnd.neighborhood_definition_id = nd.neighborhood_definition_id
			AND pnd.legal_valid_range @> $1::timestamptz
		LEFT JOIN public.neighborhoods n 
			ON pnd.neighborhood_id = n.neighborhood_id

		-- Aggregated Affordances Subquery
		LEFT JOIN (
			SELECT 
				pa.parcel_id,
				array_remove(array_agg(DISTINCT z.public_id::text), NULL) AS zoning_public_ids,
				array_remove(array_agg(DISTINCT pa.public_id::text), NULL) AS affordance_ids,
				MIN(max_far) AS strict_max_far,
				MAX(min_lot_size_sq_m) AS strict_min_lot_size_sq_m,
				MIN(max_height_m) AS strict_max_height_m,
				MIN(max_dwelling_units_per_hectare) AS strict_max_dwelling_units_per_hectare,
				MIN(max_lot_coverage_pct) AS strict_max_lot_coverage_pct
			FROM public.parcel_affordances pa
			LEFT JOIN public.zoning z ON pa.zoning_id = z.zoning_id
			WHERE pa.parcel_id = ANY(ARRAY(SELECT parcel_id FROM matched_parcels))
				AND pa.legal_valid_range @> $1::timestamptz
			GROUP BY pa.parcel_id
		) aff ON p.parcel_id = aff.parcel_id

		-- Aggregated Parcel Valuations Subquery
		LEFT JOIN (
			SELECT 
				pv.parcel_id,
				pv.market_value::numeric(19,4)::text AS market_land_value,
				pv.assessed_value::numeric(19,4)::text AS assessed_land_value
			FROM public.parcel_valuations pv
			JOIN public.valuations v ON pv.valuation_id = v.valuation_id
			WHERE $2::uuid IS NOT NULL 
				AND v.public_id = $2::uuid
				AND pv.legal_valid_range @> $1::timestamptz
		) pv_agg ON p.parcel_id = pv_agg.parcel_id

		-- Improvements Joins
		LEFT JOIN imp_totals ON p.parcel_id = imp_totals.parcel_id
		LEFT JOIN primary_imp_details pid ON p.parcel_id = pid.parcel_id
	`, filterClause)

	rows, err := s.db.Query(ctx, query, targetTime, valuationIDArg, neighborhoodDefIDArg, idsArg)
	if err != nil {
		s.logger.Error("failed to query parcels with improvement summary", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve parcel data"))
	}
	defer rows.Close()

	parcels := make(map[string]*parcelsv1.ParcelWithImprovementSummary)

	for rows.Next() {
		var (
			parcelID            string
			address             *string
			addressID           *string
			primaryOwnerName    *string
			primaryOwnerAddress *string
			partyIDs            []string
			landAreaSqM         *float64
			frontageM           *float64
			depthM              *float64
			landUseID           *string
			neighborhoodID      *string

			zoningIDs               []string
			affordanceIDs           []string
			maxFar                  *float64
			minLotSizeSqM           *float64
			maxHeightM              *float64
			maxDwellingUnitsPerHect *float64
			maxLotCoveragePct       *float64

			marketLandValue   *string
			assessedLandValue *string

			improvementIDs   []string
			primaryImpID     string
			totalAreaSqM     float64
			totalBathrooms   int32
			totalBedrooms    int32
			totalUnits       int32
			primaryYearBuilt *int32
			primaryEffectiveYearBuilt *int32
			primaryCondID    *string
			marketImpValue   *string
			assessedImpValue *string

			properties *string
			featureID  int64
		)

		err := rows.Scan(
			&parcelID,
			&address,
			&addressID,
			&primaryOwnerName,
			&primaryOwnerAddress,
			&partyIDs,
			&landAreaSqM,
			&frontageM,
			&depthM,
			&landUseID,
			&neighborhoodID,

			&zoningIDs,
			&affordanceIDs,
			&maxFar,
			&minLotSizeSqM,
			&maxHeightM,
			&maxDwellingUnitsPerHect,
			&maxLotCoveragePct,

			&marketLandValue,
			&assessedLandValue,

			&improvementIDs,
			&primaryImpID,
			&totalAreaSqM,
			&totalBathrooms,
			&totalBedrooms,
			&totalUnits,
			&primaryYearBuilt,
			&primaryEffectiveYearBuilt,
			&primaryCondID,
			&marketImpValue,
			&assessedImpValue,

			&properties,
			&featureID,
		)
		if err != nil {
			s.logger.Error("failed to scan parcel row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data unmarshaling error"))
		}

		var landAreaSqFt, frontageFt, depthFt *float64
		if landAreaSqM != nil {
			val := *landAreaSqM * 10.7639
			landAreaSqFt = &val
		}
		if frontageM != nil {
			val := *frontageM * 3.28084
			frontageFt = &val
		}
		if depthM != nil {
			val := *depthM * 3.28084
			depthFt = &val
		}

		totalAreaSqFt := totalAreaSqM * 10.7639

		if zoningIDs == nil {
			zoningIDs = []string{}
		}
		if partyIDs == nil {
			partyIDs = []string{}
		}
		if improvementIDs == nil {
			improvementIDs = []string{}
		}

		parcels[parcelID] = &parcelsv1.ParcelWithImprovementSummary{
			ParcelDetails: &parcelsv1.ParcelDetails{
				ParcelId:            parcelID,
				FeatureId:           &featureID,
				FormattedAddress:    address,
				AddressId:           addressID,
				PrimaryOwnerName:    primaryOwnerName,
				PrimaryOwnerAddress: primaryOwnerAddress,
				PartyIds:            partyIDs,
				LandUseId:           landUseID,
				NeighborhoodId:      neighborhoodID,
				LandAreaSqFt:        landAreaSqFt,
				FrontageFt:          frontageFt,
				DepthFt:             depthFt,
				ZoningIds:           zoningIDs,
				MarketLandValue:     marketLandValue,
				AssessedLandValue:   assessedLandValue,
				Properties:          properties,
			},
			ImprovementSummary: &parcelsv1.ParcelImprovementsSummary{
				ImprovementIds:                improvementIDs,
				PrimaryImprovementId:          primaryImpID,
				TotalAreaSqFt:                 totalAreaSqFt,
				TotalBathrooms:                totalBathrooms,
				TotalBedrooms:                 totalBedrooms,
				TotalUnits:                    totalUnits,
				PrimaryYearBuilt:              primaryYearBuilt,
				PrimaryEffectiveYearBuilt:     primaryEffectiveYearBuilt,
				PrimaryConditionId:            primaryCondID,
				TotalMarketImprovementValue:   marketImpValue,
				TotalAssessedImprovementValue: assessedImpValue,
			},
		}
	}

	if err := rows.Err(); err != nil {
		s.logger.Error("error iterating parcel rows", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to process parcel stream"))
	}

	return parcels, nil
}

func (s *APIServer) UpdateParcel(
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

func (s *APIServer) GetCategoricalParcelStatsById(
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

func (s *APIServer) GetNumericalParcelStatsById(
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

type CompParcelInfo struct {
	ParcelID            string
	AddressID           string
	FormattedAddress    string
	LandAreaSqFt        *float64
	FrontageFt          *float64
	DepthFt             *float64
	LandUseID           *string
	ZoningIDs           []string
	ImprovementAreaSqFt float64
	YearBuilt           *int32
	EffectiveYearBuilt  *int32
	Bedrooms            int32
	Bathrooms           int32
	Units               int32
	ConditionIDs        []string
	ImprovementTypeIDs  []string
	SaleTime            *time.Time
	SalePrice           *string
	FeatureID           int64
}

// fetchParcelsForComps retrieves candidate parcels and their metadata attributes.
// To maximize performance and scale efficiently, it dynamically compiles the SQL query,
// appending SELECT columns, LEFT JOIN clauses, and row scan destinations (scanDest)
// ONLY for the attributes specified in the criteria list or if sales information is requested.
func (s *APIServer) fetchParcelsForComps(
	ctx context.Context,
	candidateIDs []string,
	wktPolygon *string,
	startTime *time.Time,
	endTime *time.Time,
	criteria []*parcelsv1.ComparableCriteria,
	isSales bool,
) (map[string]*CompParcelInfo, error) {

	// Baseline fields that are always queried for every comparable parcel
	var selectFields = []string{
		"p.public_id::text",
		"LEFT(aa.formatted_address, 1024) AS safe_address",
		"a.public_id::text",
		"pa.land_area_sq_m",
		"pa.frontage_m",
		"pa.depth_m",
		"COALESCE(pg.feature_id, 0)",
	}

	// Baseline joins required for the default parcel attributes
	var joins = []string{
		"LEFT JOIN parcel_attributes pa ON p.parcel_id = pa.parcel_id",
		"LEFT JOIN addresses a ON pa.address_id = a.address_id",
		"LEFT JOIN address_attributes aa ON a.address_id = aa.address_id",
		"LEFT JOIN parcel_geometry pg ON p.parcel_id = pg.parcel_id",
	}

	// Variables where scanned column data is stored
	var (
		parcelID                  string
		address                   *string
		addressID                 *string
		landAreaSqM               *float64
		frontageM                 *float64
		depthM                    *float64
		landUseID                 *string
		zoningIDs                 []string
		totalAreaSqM              float64
		totalBathrooms            int32
		totalBedrooms             int32
		totalUnits                int32
		primaryYearBuilt          *int32
		primaryEffectiveYearBuilt *int32
		primaryConditionID        *string
		primaryImprovementTypeID  *string
		saleTime                  *time.Time
		salePrice                 *string
		featureID                 int64
	)

	// Baseline pointers matching the order of selectFields
	var scanDest = []any{
		&parcelID,
		&address,
		&addressID,
		&landAreaSqM,
		&frontageM,
		&depthM,
		&featureID,
	}

	var joinedImprovements, joinedPrimaryImp, joinedZoning, joinedLandUse bool

	// Dynamic Query Assembly based on the input criteria
	for _, c := range criteria {
		if c == nil {
			continue
		}
		switch c.GetAttribute() {
		case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_USE_ID:
			// Add land use mapping and fetch the public ID
			if !joinedLandUse {
				joins = append(joins, "LEFT JOIN land_uses lu ON pa.land_use_id = lu.land_use_id")
				selectFields = append(selectFields, "lu.public_id::text")
				scanDest = append(scanDest, &landUseID)
				joinedLandUse = true
			}

		case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_ZONING_ID:
			// Aggregate all unique zoning IDs for the parcel
			if !joinedZoning {
				joins = append(joins, `
                    LEFT JOIN (
                        SELECT 
                            parcel_id,
                            array_remove(array_agg(DISTINCT z.public_id::text), NULL) AS zoning_public_ids
                        FROM parcel_affordances pa
                        LEFT JOIN zoning z ON pa.zoning_id = z.zoning_id
                        GROUP BY parcel_id
                    ) aff ON p.parcel_id = aff.parcel_id`)
				selectFields = append(selectFields, "aff.zoning_public_ids")
				scanDest = append(scanDest, &zoningIDs)
				joinedZoning = true
			}

		case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_AREA_SQ_FT,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BEDROOMS,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BATHROOMS,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_UNITS:
			// Join improvement attributes subquery to fetch size, rooms, and units
			if !joinedImprovements {
				joins = append(joins, `
                    LEFT JOIN (
                        SELECT 
                            pi.parcel_id,
                            COALESCE(SUM(attr.area_sq_m), 0) AS total_area_sq_m,
                            COALESCE(SUM(attr.bathrooms), 0) AS total_bathrooms,
                            COALESCE(SUM(attr.bedrooms), 0) AS total_bedrooms,
                            COALESCE(SUM(attr.units), 0) AS total_units
                        FROM parcel_improvements pi
                        JOIN improvements imp ON pi.improvement_id = imp.improvement_id
                        LEFT JOIN improvement_attributes attr ON imp.improvement_id = attr.improvement_id
                        WHERE NOT imp.is_voided
                        GROUP BY pi.parcel_id
                    ) imp_agg ON p.parcel_id = imp_agg.parcel_id`)
				selectFields = append(selectFields,
					"COALESCE(imp_agg.total_area_sq_m, 0)",
					"COALESCE(imp_agg.total_bathrooms, 0)",
					"COALESCE(imp_agg.total_bedrooms, 0)",
					"COALESCE(imp_agg.total_units, 0)",
				)
				scanDest = append(scanDest,
					&totalAreaSqM,
					&totalBathrooms,
					&totalBedrooms,
					&totalUnits,
				)
				joinedImprovements = true
			}

		case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_YEAR_BUILT,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_EFFECTIVE_YEAR_BUILT,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_CONDITION_ID,
			parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_TYPE_ID:
			// Join primary improvement attributes subquery
			if !joinedPrimaryImp {
				joins = append(joins, `
                    LEFT JOIN LATERAL (
                        SELECT 
                            attr.year_built AS primary_year_built,
                            attr.effective_year_built AS primary_effective_year_built,
                            cond.public_id::text AS primary_condition_id,
                            imptype.public_id::text AS primary_improvement_type_id
                        FROM public.get_primary_improvements(ARRAY[p.parcel_id], NOW()) pimp
                        JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
                        LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id 
                            AND attr.legal_valid_range @> NOW()
                        LEFT JOIN public.improvement_types imptype ON attr.improvement_type_id = imptype.improvement_type_id
                        LEFT JOIN public.improvement_conditions cond ON attr.improvement_condition_id = cond.improvement_condition_id 
                            AND cond.is_voided = false
                        WHERE imp.is_voided = false
                    ) prim_imp ON true`)
				selectFields = append(selectFields,
					"prim_imp.primary_year_built",
					"prim_imp.primary_effective_year_built",
					"prim_imp.primary_condition_id",
					"prim_imp.primary_improvement_type_id",
				)
				scanDest = append(scanDest,
					&primaryYearBuilt,
					&primaryEffectiveYearBuilt,
					&primaryConditionID,
					&primaryImprovementTypeID,
				)
				joinedPrimaryImp = true
			}
		}
	}

	// For sales comparables, join real property transfers to get the latest sales transaction
	if isSales {
		joins = append(joins, `
            LEFT JOIN (
                SELECT DISTINCT ON (rtpp.parcel_id)
                    rtpp.parcel_id,
                    rpt.transfer_timestamp,
                    rpt.transfer_amount::numeric(19,4)::text AS sale_price
                FROM real_property_transfer_party_parcels rtpp
                JOIN real_property_transfers rpt ON rtpp.real_property_transfer_id = rpt.real_property_transfer_id
                WHERE ($3::timestamptz IS NULL OR rpt.transfer_timestamp >= $3::timestamptz)
                  AND ($4::timestamptz IS NULL OR rpt.transfer_timestamp <= $4::timestamptz)
                ORDER BY rtpp.parcel_id, rpt.transfer_timestamp DESC
            ) sales_agg ON p.parcel_id = sales_agg.parcel_id`)
		selectFields = append(selectFields, "sales_agg.transfer_timestamp", "sales_agg.sale_price")
		scanDest = append(scanDest, &saleTime, &salePrice)
	}

	// SQL Query construction: filters either strictly by selected public IDs or geographically by ST_Intersects
	var filterCond string
	if len(candidateIDs) > 0 {
		filterCond = "($1::uuid[] IS NOT NULL AND p.public_id = ANY($1::uuid[]) AND ($2::text IS NULL OR TRUE))"
	} else {
		filterCond = "($1::uuid[] IS NULL AND $2::text IS NOT NULL AND ST_Intersects(pg.geom_web, ST_GeomFromText($2, 4326)))"
	}

	query := fmt.Sprintf(`
        SELECT 
            %s
        FROM parcels p
        %s
        WHERE NOT p.is_voided
          AND %s
    `, strings.Join(selectFields, ",\n"), strings.Join(joins, "\n"), filterCond)

	s.logger.Debug("performing dynamic comps query", slog.String("sql", query))

	var candidateIDsArg *[]string
	if len(candidateIDs) > 0 {
		candidateIDsArg = &candidateIDs
	}

	// Build exact query arguments list to prevent prepared statement parameter count mismatches (SQLSTATE 08P01)
	var queryArgs = []any{candidateIDsArg, wktPolygon}
	if isSales {
		queryArgs = append(queryArgs, startTime, endTime)
	}

	rows, err := s.db.Query(ctx, query, queryArgs...)
	if err != nil {
		s.logger.Error("failed to query parcels for comps", slog.Any("error", err))
		return nil, fmt.Errorf("failed to retrieve comp parcel data: %w", err)
	}
	defer rows.Close()

	parcels := make(map[string]*CompParcelInfo)
	for rows.Next() {
		err := rows.Scan(scanDest...)
		if err != nil {
			s.logger.Error("failed to scan comp parcel row", slog.Any("error", err))
			return nil, fmt.Errorf("data unmarshaling error in comps query: %w", err)
		}

		// Unit conversions: Convert metric database columns to imperial for the Protobuf API contract
		var landAreaSqFt, frontageFt, depthFt *float64
		if landAreaSqM != nil {
			// Convert square meters to square feet (1 sq m = 10.7639 sq ft)
			val := *landAreaSqM * 10.7639
			landAreaSqFt = &val
		}
		if frontageM != nil {
			// Convert meters to feet (1 m = 3.28084 ft)
			val := *frontageM * 3.28084
			frontageFt = &val
		}
		if depthM != nil {
			// Convert meters to feet (1 m = 3.28084 ft)
			val := *depthM * 3.28084
			depthFt = &val
		}
		// Convert total building/improvement area to square feet
		totalAreaSqFt := totalAreaSqM * 10.7639

		// Primary improvement fields map directly
		var yearBuilt *int32
		if primaryYearBuilt != nil {
			yearBuilt = primaryYearBuilt
		}

		var effectiveYearBuilt *int32
		if primaryEffectiveYearBuilt != nil {
			effectiveYearBuilt = primaryEffectiveYearBuilt
		}

		// Prevent nil slice issues by returning empty slices instead of null in JSON response
		if zoningIDs == nil {
			zoningIDs = []string{}
		}

		var conditionIDs []string
		if primaryConditionID != nil && *primaryConditionID != "" {
			conditionIDs = []string{*primaryConditionID}
		} else {
			conditionIDs = []string{}
		}

		var improvementTypeIDs []string
		if primaryImprovementTypeID != nil && *primaryImprovementTypeID != "" {
			improvementTypeIDs = []string{*primaryImprovementTypeID}
		} else {
			improvementTypeIDs = []string{}
		}

		addrID := ""
		if addressID != nil {
			addrID = *addressID
		}
		addrStr := ""
		if address != nil {
			addrStr = *address
		}

		parcels[parcelID] = &CompParcelInfo{
			ParcelID:            parcelID,
			AddressID:           addrID,
			FormattedAddress:    addrStr,
			LandAreaSqFt:        landAreaSqFt,
			FrontageFt:          frontageFt,
			DepthFt:             depthFt,
			LandUseID:           landUseID,
			ZoningIDs:           zoningIDs,
			ImprovementAreaSqFt: totalAreaSqFt,
			YearBuilt:           yearBuilt,
			EffectiveYearBuilt:  effectiveYearBuilt,
			Bedrooms:            totalBedrooms,
			Bathrooms:           totalBathrooms,
			Units:               totalUnits,
			ConditionIDs:        conditionIDs,
			ImprovementTypeIDs:  improvementTypeIDs,
			SaleTime:            saleTime,
			SalePrice:           salePrice,
			FeatureID:           featureID,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading parcel rows: %w", err)
	}

	return parcels, nil
}

func getNumericalValue(p *CompParcelInfo, attr parcelsv1.ParcelAttribute) (*float64, bool) {
	switch attr {
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_AREA_SQ_FT:
		return p.LandAreaSqFt, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_FRONTAGE_FT:
		return p.FrontageFt, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_DEPTH_FT:
		return p.DepthFt, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_IMPROVEMENT_AREA_SQ_FT:
		val := p.ImprovementAreaSqFt
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_YEAR_BUILT:
		if p.YearBuilt == nil {
			return nil, true
		}
		val := float64(*p.YearBuilt)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_EFFECTIVE_YEAR_BUILT:
		if p.EffectiveYearBuilt == nil {
			return nil, true
		}
		val := float64(*p.EffectiveYearBuilt)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BEDROOMS:
		val := float64(p.Bedrooms)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_BATHROOMS:
		val := float64(p.Bathrooms)
		return &val, true
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_UNITS:
		val := float64(p.Units)
		return &val, true
	}
	return nil, false
}

func checkCategoricalMatch(cand *CompParcelInfo, attr parcelsv1.ParcelAttribute, tolerance []string) bool {
	switch attr {
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_USE_ID:
		candVal := ""
		if cand.LandUseID != nil {
			candVal = *cand.LandUseID
		}
		for _, t := range tolerance {
			if t == candVal {
				return true
			}
		}
		return false

	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_ZONING_ID:
		return sliceOverlap(cand.ZoningIDs, tolerance)

	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_CONDITION_ID:
		return sliceOverlap(cand.ConditionIDs, tolerance)

	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_TYPE_ID:
		return sliceOverlap(cand.ImprovementTypeIDs, tolerance)
	}
	return false
}

func sliceOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func getCategoricalValueString(p *CompParcelInfo, attr parcelsv1.ParcelAttribute) *string {
	switch attr {
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_LAND_USE_ID:
		return p.LandUseID
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_ZONING_ID:
		if len(p.ZoningIDs) == 0 {
			return nil
		}
		val := strings.Join(p.ZoningIDs, ",")
		return &val
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_CONDITION_ID:
		if len(p.ConditionIDs) == 0 {
			return nil
		}
		val := strings.Join(p.ConditionIDs, ",")
		return &val
	case parcelsv1.ParcelAttribute_PARCEL_ATTRIBUTE_PRIMARY_IMPROVEMENT_TYPE_ID:
		if len(p.ImprovementTypeIDs) == 0 {
			return nil
		}
		val := strings.Join(p.ImprovementTypeIDs, ",")
		return &val
	}
	return nil
}

func (s *APIServer) GetEquityComparables(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetEquityComparablesRequest],
) (*connect.Response[parcelsv1.GetEquityComparablesResponse], error) {

	s.logger.Debug("received GetEquityComparables request",
		slog.Any("selected_parcel_ids", req.Msg.SelectedParcelIds),
		slog.String("wkt_polygon", req.Msg.WktPolygon))

	if len(req.Msg.SelectedParcelIds) == 0 && req.Msg.GetWktPolygon() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("either selected_parcel_ids or wkt_polygon must be provided"))
	}

	var candidateIDs []string
	var polygonArg *string

	if len(req.Msg.SelectedParcelIds) > 0 {
		candidateIDs = req.Msg.SelectedParcelIds
	} else {
		if p := req.Msg.GetWktPolygon(); p != "" {
			polygonArg = &p
		}
	}

	parcelsMap, err := s.fetchParcelsForComps(ctx, candidateIDs, polygonArg, nil, nil, req.Msg.Criteria, false)
	if err != nil {
		s.logger.Error("failed to fetch parcels for equity comps", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}

	resParcels := make(map[string]*parcelsv1.EquityComparableParcel)

	for id, cand := range parcelsMap {
		matched := true
		var attrs []*parcelsv1.ComparableAttribute

		for _, crit := range req.Msg.Criteria {
			if crit == nil {
				continue
			}

			attr := crit.GetAttribute()
			numVal, isNum := getNumericalValue(cand, attr)
			if isNum {
				if numVal == nil {
					matched = false
					break
				}
				if crit.MinNumericalTolerance != nil {
					if *numVal < *crit.MinNumericalTolerance {
						matched = false
						break
					}
				}
				if crit.MaxNumericalTolerance != nil {
					if *numVal > *crit.MaxNumericalTolerance {
						matched = false
						break
					}
				}
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:      attr,
					NumericalValue: numVal,
				})
			} else {
				if len(crit.GetCategoricalTolerance()) > 0 {
					if !checkCategoricalMatch(cand, attr, crit.GetCategoricalTolerance()) {
						matched = false
						break
					}
				}
				catStr := getCategoricalValueString(cand, attr)
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:        attr,
					CategoricalValue: catStr,
				})
			}
		}

		if matched {
			resParcels[id] = &parcelsv1.EquityComparableParcel{
				ParcelId:         cand.ParcelID,
				FeatureId:        cand.FeatureID,
				AddressId:        cand.AddressID,
				FormattedAddress: cand.FormattedAddress,
				Attributes:       attrs,
			}
		}
	}

	return connect.NewResponse(&parcelsv1.GetEquityComparablesResponse{
		Parcels: resParcels,
	}), nil
}

func (s *APIServer) GetSalesComparables(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetSalesComparablesRequest],
) (*connect.Response[parcelsv1.GetSalesComparablesResponse], error) {

	s.logger.Debug("received GetSalesComparables request",
		slog.Any("selected_parcel_ids", req.Msg.SelectedParcelIds),
		slog.String("wkt_polygon", req.Msg.WktPolygon))

	if len(req.Msg.SelectedParcelIds) == 0 && req.Msg.GetWktPolygon() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("either selected_parcel_ids or wkt_polygon must be provided"))
	}

	var candidateIDs []string
	var polygonArg *string

	if len(req.Msg.SelectedParcelIds) > 0 {
		candidateIDs = req.Msg.SelectedParcelIds
	} else {
		if p := req.Msg.GetWktPolygon(); p != "" {
			polygonArg = &p
		}
	}

	var startTime, endTime *time.Time
	if tr := req.Msg.GetTimeRange(); tr != nil {
		if st := tr.GetStartTime(); st != nil {
			t := st.AsTime()
			startTime = &t
		}
		if et := tr.GetEndTime(); et != nil {
			t := et.AsTime()
			endTime = &t
		}
	}

	parcelsMap, err := s.fetchParcelsForComps(ctx, candidateIDs, polygonArg, startTime, endTime, req.Msg.Criteria, true)
	if err != nil {
		s.logger.Error("failed to fetch parcels for sales comps", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}

	resParcels := make(map[string]*parcelsv1.SaleComparableParcel)

	for id, cand := range parcelsMap {
		if cand.SaleTime == nil || cand.SalePrice == nil {
			continue
		}

		matched := true
		var attrs []*parcelsv1.ComparableAttribute

		for _, crit := range req.Msg.Criteria {
			if crit == nil {
				continue
			}

			attr := crit.GetAttribute()
			numVal, isNum := getNumericalValue(cand, attr)
			if isNum {
				if numVal == nil {
					matched = false
					break
				}
				if crit.MinNumericalTolerance != nil {
					if *numVal < *crit.MinNumericalTolerance {
						matched = false
						break
					}
				}
				if crit.MaxNumericalTolerance != nil {
					if *numVal > *crit.MaxNumericalTolerance {
						matched = false
						break
					}
				}
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:      attr,
					NumericalValue: numVal,
				})
			} else {
				if len(crit.GetCategoricalTolerance()) > 0 {
					if !checkCategoricalMatch(cand, attr, crit.GetCategoricalTolerance()) {
						matched = false
						break
					}
				}
				catStr := getCategoricalValueString(cand, attr)
				attrs = append(attrs, &parcelsv1.ComparableAttribute{
					Attribute:        attr,
					CategoricalValue: catStr,
				})
			}
		}

		if matched {
			resParcels[id] = &parcelsv1.SaleComparableParcel{
				ParcelId:         cand.ParcelID,
				FeatureId:        cand.FeatureID,
				AddressId:        cand.AddressID,
				FormattedAddress: cand.FormattedAddress,
				SaleTime:         timestamppb.New(*cand.SaleTime),
				SalePrice:        *cand.SalePrice,
				Attributes:       attrs,
			}
		}
	}

	return connect.NewResponse(&parcelsv1.GetSalesComparablesResponse{
		Parcels: resParcels,
	}), nil
}

func (s *APIServer) GetParcelIdsByFeatureId(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelIdsByFeatureIdRequest],
) (*connect.Response[parcelsv1.GetParcelIdsByFeatureIdResponse], error) {

	s.logger.Debug("received GetParcelIdsByFeatureId request", slog.Any("featureIds", req.Msg.FeatureIds))

	featureIDs := req.Msg.FeatureIds
	if len(featureIDs) == 0 {
		return connect.NewResponse(&parcelsv1.GetParcelIdsByFeatureIdResponse{
			ParcelIds: make(map[int64]string),
		}), nil
	}

	query := `
		SELECT pg.feature_id, p.public_id::text
		FROM parcel_geometry pg
		JOIN parcels p ON pg.parcel_id = p.parcel_id
		WHERE pg.feature_id = ANY($1::bigint[]) AND NOT p.is_voided
	`

	rows, err := s.db.Query(ctx, query, featureIDs)
	if err != nil {
		s.logger.Error("failed to query parcel IDs by feature ID", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve parcel IDs"))
	}
	defer rows.Close()

	parcelIDsMap := make(map[int64]string)
	for rows.Next() {
		var featureID int64
		var parcelUUID string
		if err := rows.Scan(&featureID, &parcelUUID); err != nil {
			s.logger.Error("failed to scan parcel ID row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data unmarshaling error"))
		}
		parcelIDsMap[featureID] = parcelUUID
	}

	if err := rows.Err(); err != nil {
		s.logger.Error("error iterating parcel ID rows", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to process parcel ID stream"))
	}

	return connect.NewResponse(&parcelsv1.GetParcelIdsByFeatureIdResponse{
		ParcelIds: parcelIDsMap,
	}), nil
}

func (s *APIServer) GetEstimatedParcelsExtentWGS84(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetEstimatedParcelsExtentWGS84Request],
) (*connect.Response[parcelsv1.GetEstimatedParcelsExtentWGS84Response], error) {

	s.logger.Debug("received GetEstimatedParcelsExtentWGS84 request")

	var minX, minY, maxX, maxY float64
	query := `
		SELECT 
			COALESCE(ST_XMin(ext), 0.0), 
			COALESCE(ST_YMin(ext), 0.0), 
			COALESCE(ST_XMax(ext), 0.0), 
			COALESCE(ST_YMax(ext), 0.0) 
		FROM (
			SELECT ST_EstimatedExtent('parcel_geometry', 'geom_web') AS ext
		) AS sub
	`
	err := s.db.QueryRow(ctx, query).Scan(&minX, &minY, &maxX, &maxY)
	if err != nil {
		s.logger.Warn("ST_EstimatedExtent failed, falling back to ST_Extent", slog.Any("error", err))
		fallbackQuery := `
			SELECT 
				COALESCE(ST_XMin(ST_Extent(geom_web)), 0.0), 
				COALESCE(ST_YMin(ST_Extent(geom_web)), 0.0), 
				COALESCE(ST_XMax(ST_Extent(geom_web)), 0.0), 
				COALESCE(ST_YMax(ST_Extent(geom_web)), 0.0) 
			FROM parcel_geometry
		`
		err = s.db.QueryRow(ctx, fallbackQuery).Scan(&minX, &minY, &maxX, &maxY)
		if err != nil {
			s.logger.Error("fallback ST_Extent query failed", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to retrieve estimated parcels extent"))
		}
	}

	res := &parcelsv1.GetEstimatedParcelsExtentWGS84Response{
		MinX: minX,
		MinY: minY,
		MaxX: maxX,
		MaxY: maxY,
	}

	return connect.NewResponse(res), nil
}

func getTargetTime(legalAsOf *timestamppb.Timestamp) time.Time {
	if legalAsOf != nil {
		return legalAsOf.AsTime()
	}
	return time.Now()
}

func (s *APIServer) GetLandAreaSqftByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetLandAreaSqftByParcelIdRequest]) (*connect.Response[parcelsv1.GetLandAreaSqftByParcelIdResponse], error) {
	values, err := s.getLandAreaSqftByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetLandAreaSqftByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getLandAreaSqftByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]float64, error) {
	values := make(map[string]float64)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, pa.land_area_sq_m
		FROM public.parcels p
		JOIN public.parcel_attributes pa ON p.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		WHERE p.public_id = ANY($2::uuid[])
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var areaSqM *float64
		if err := rows.Scan(&pid, &areaSqM); err != nil { return nil, err }
		if areaSqM != nil {
			values[pid] = *areaSqM * 10.7639
		}
	}
	return values, nil
}

func (s *APIServer) GetFrontageFtByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetFrontageFtByParcelIdRequest]) (*connect.Response[parcelsv1.GetFrontageFtByParcelIdResponse], error) {
	values, err := s.getFrontageFtByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetFrontageFtByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getFrontageFtByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]float64, error) {
	values := make(map[string]float64)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, pa.frontage_m
		FROM public.parcels p
		JOIN public.parcel_attributes pa ON p.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		WHERE p.public_id = ANY($2::uuid[])
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var frontageM *float64
		if err := rows.Scan(&pid, &frontageM); err != nil { return nil, err }
		if frontageM != nil {
			values[pid] = *frontageM * 3.28084
		}
	}
	return values, nil
}

func (s *APIServer) GetDepthFtByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetDepthFtByParcelIdRequest]) (*connect.Response[parcelsv1.GetDepthFtByParcelIdResponse], error) {
	values, err := s.getDepthFtByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetDepthFtByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getDepthFtByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]float64, error) {
	values := make(map[string]float64)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, pa.depth_m
		FROM public.parcels p
		JOIN public.parcel_attributes pa ON p.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		WHERE p.public_id = ANY($2::uuid[])
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var depthM *float64
		if err := rows.Scan(&pid, &depthM); err != nil { return nil, err }
		if depthM != nil {
			values[pid] = *depthM * 3.28084
		}
	}
	return values, nil
}

func (s *APIServer) GetLandUseIdSqftByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetLandUseIdSqftByParcelIdRequest]) (*connect.Response[parcelsv1.GetLandUseIdSqftByParcelIdResponse], error) {
	values, err := s.getLandUseIdSqftByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetLandUseIdSqftByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getLandUseIdSqftByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]string, error) {
	values := make(map[string]string)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, lu.public_id::text
		FROM public.parcels p
		JOIN public.parcel_attributes pa ON p.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		JOIN public.land_uses lu ON pa.land_use_id = lu.land_use_id
		WHERE p.public_id = ANY($2::uuid[])
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var luId *string
		if err := rows.Scan(&pid, &luId); err != nil { return nil, err }
		if luId != nil {
			values[pid] = *luId
		}
	}
	return values, nil
}

func (s *APIServer) GetZoningIdByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetZoningIdByParcelIdRequest]) (*connect.Response[parcelsv1.GetZoningIdByParcelIdResponse], error) {
	values, err := s.getZoningIdByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetZoningIdByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getZoningIdByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]string, error) {
	values := make(map[string]string)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, (array_remove(array_agg(DISTINCT z.public_id::text), NULL))[1]
		FROM public.parcels p
		JOIN public.parcel_affordances pa ON p.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		JOIN public.zoning z ON pa.zoning_id = z.zoning_id
		WHERE p.public_id = ANY($2::uuid[])
		GROUP BY p.public_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var zoningId *string
		if err := rows.Scan(&pid, &zoningId); err != nil { return nil, err }
		if zoningId != nil {
			values[pid] = *zoningId
		}
	}
	return values, nil
}

func (s *APIServer) GetImprovementAreaSqftByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetImprovementAreaSqftByParcelIdRequest]) (*connect.Response[parcelsv1.GetImprovementAreaSqftByParcelIdResponse], error) {
	values, err := s.getImprovementAreaSqftByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetImprovementAreaSqftByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getImprovementAreaSqftByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]float64, error) {
	values := make(map[string]float64)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, COALESCE(SUM(attr.area_sq_m), 0.0)
		FROM public.parcels p
		JOIN public.parcel_improvements pi ON p.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE p.public_id = ANY($2::uuid[]) AND imp.is_voided = false
		GROUP BY p.public_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var areaSqM float64
		if err := rows.Scan(&pid, &areaSqM); err != nil { return nil, err }
		values[pid] = areaSqM * 10.7639
	}
	return values, nil
}

func (s *APIServer) GetBedroomsByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetBedroomsByParcelIdRequest]) (*connect.Response[parcelsv1.GetBedroomsByParcelIdResponse], error) {
	values, err := s.getBedroomsByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetBedroomsByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getBedroomsByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]int32, error) {
	values := make(map[string]int32)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, COALESCE(SUM(attr.bedrooms), 0)::int
		FROM public.parcels p
		JOIN public.parcel_improvements pi ON p.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE p.public_id = ANY($2::uuid[]) AND imp.is_voided = false
		GROUP BY p.public_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var bedrooms int32
		if err := rows.Scan(&pid, &bedrooms); err != nil { return nil, err }
		values[pid] = bedrooms
	}
	return values, nil
}

func (s *APIServer) GetBathroomsByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetBathroomsByParcelIdRequest]) (*connect.Response[parcelsv1.GetBathroomsByParcelIdResponse], error) {
	values, err := s.getBathroomsByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetBathroomsByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getBathroomsByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]int32, error) {
	values := make(map[string]int32)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, COALESCE(SUM(attr.bathrooms), 0)::int
		FROM public.parcels p
		JOIN public.parcel_improvements pi ON p.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE p.public_id = ANY($2::uuid[]) AND imp.is_voided = false
		GROUP BY p.public_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var bathrooms int32
		if err := rows.Scan(&pid, &bathrooms); err != nil { return nil, err }
		values[pid] = bathrooms
	}
	return values, nil
}

func (s *APIServer) GetUnitsByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetUnitsByParcelIdRequest]) (*connect.Response[parcelsv1.GetUnitsByParcelIdResponse], error) {
	values, err := s.getUnitsByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetUnitsByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getUnitsByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]int32, error) {
	values := make(map[string]int32)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, COALESCE(SUM(attr.units), 0)::int
		FROM public.parcels p
		JOIN public.parcel_improvements pi ON p.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE p.public_id = ANY($2::uuid[]) AND imp.is_voided = false
		GROUP BY p.public_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var units int32
		if err := rows.Scan(&pid, &units); err != nil { return nil, err }
		values[pid] = units
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementYearBuiltByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementYearBuiltByParcelIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementYearBuiltByParcelIdResponse], error) {
	values, err := s.getPrimaryImprovementYearBuiltByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementYearBuiltByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementYearBuiltByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]int32, error) {
	values := make(map[string]int32)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, attr.year_built
		FROM public.parcels p
		JOIN public.get_primary_improvements(ARRAY(
			SELECT p2.parcel_id FROM public.parcels p2 WHERE p2.public_id = ANY($2::uuid[])
		), $1::timestamptz) pimp ON p.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE imp.is_voided = false
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var yearBuilt *int32
		if err := rows.Scan(&pid, &yearBuilt); err != nil { return nil, err }
		if yearBuilt != nil {
			values[pid] = *yearBuilt
		}
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementEffectiveYearBuiltByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementEffectiveYearBuiltByParcelIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementEffectiveYearBuiltByParcelIdResponse], error) {
	values, err := s.getPrimaryImprovementEffectiveYearBuiltByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementEffectiveYearBuiltByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementEffectiveYearBuiltByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]int32, error) {
	values := make(map[string]int32)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, attr.effective_year_built
		FROM public.parcels p
		JOIN public.get_primary_improvements(ARRAY(
			SELECT p2.parcel_id FROM public.parcels p2 WHERE p2.public_id = ANY($2::uuid[])
		), $1::timestamptz) pimp ON p.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE imp.is_voided = false
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var effYearBuilt *int32
		if err := rows.Scan(&pid, &effYearBuilt); err != nil { return nil, err }
		if effYearBuilt != nil {
			values[pid] = *effYearBuilt
		}
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementConditionIdByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementConditionIdByParcelIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementConditionIdByParcelIdResponse], error) {
	values, err := s.getPrimaryImprovementConditionIdByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementConditionIdByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementConditionIdByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]string, error) {
	values := make(map[string]string)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, cond.public_id::text
		FROM public.parcels p
		JOIN public.get_primary_improvements(ARRAY(
			SELECT p2.parcel_id FROM public.parcels p2 WHERE p2.public_id = ANY($2::uuid[])
		), $1::timestamptz) pimp ON p.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		LEFT JOIN public.improvement_conditions cond ON attr.improvement_condition_id = cond.improvement_condition_id AND cond.is_voided = false
		WHERE imp.is_voided = false
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var condId *string
		if err := rows.Scan(&pid, &condId); err != nil { return nil, err }
		if condId != nil {
			values[pid] = *condId
		}
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementTypeIdByParcelId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementTypeIdByParcelIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementTypeIdByParcelIdResponse], error) {
	values, err := s.getPrimaryImprovementTypeIdByParcelId(ctx, req.Msg.ParcelIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementTypeIdByParcelIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementTypeIdByParcelId(ctx context.Context, parcelIds []string, legalAsOf *timestamppb.Timestamp) (map[string]string, error) {
	values := make(map[string]string)
	if len(parcelIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT p.public_id::text, imptype.public_id::text
		FROM public.parcels p
		JOIN public.get_primary_improvements(ARRAY(
			SELECT p2.parcel_id 
			FROM public.parcels p2 
			WHERE p2.public_id = ANY($2::uuid[])
		), $1::timestamptz) pimp ON p.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id 
			AND attr.legal_valid_range @> $1::timestamptz
		LEFT JOIN public.improvement_types imptype ON attr.improvement_type_id = imptype.improvement_type_id
		WHERE imp.is_voided = false
	`
	rows, err := s.db.Query(ctx, query, targetTime, parcelIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var pid string
		var typeId *string
		if err := rows.Scan(&pid, &typeId); err != nil { return nil, err }
		if typeId != nil {
			values[pid] = *typeId
		}
	}
	return values, nil
}

func (s *APIServer) GetLandAreaSqftByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetLandAreaSqftByFeatureIdRequest]) (*connect.Response[parcelsv1.GetLandAreaSqftByFeatureIdResponse], error) {
	values, err := s.getLandAreaSqftByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetLandAreaSqftByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getLandAreaSqftByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]float64, error) {
	values := make(map[int64]float64)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, pa.land_area_sq_m
		FROM public.parcel_geometry pg
		JOIN public.parcel_attributes pa ON pg.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var areaSqM *float64
		if err := rows.Scan(&fid, &areaSqM); err != nil { return nil, err }
		if areaSqM != nil {
			values[fid] = *areaSqM * 10.7639
		}
	}
	return values, nil
}

func (s *APIServer) GetFrontageFtByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetFrontageFtByFeatureIdRequest]) (*connect.Response[parcelsv1.GetFrontageFtByFeatureIdResponse], error) {
	values, err := s.getFrontageFtByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetFrontageFtByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getFrontageFtByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]float64, error) {
	values := make(map[int64]float64)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, pa.frontage_m
		FROM public.parcel_geometry pg
		JOIN public.parcel_attributes pa ON pg.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var frontageM *float64
		if err := rows.Scan(&fid, &frontageM); err != nil { return nil, err }
		if frontageM != nil {
			values[fid] = *frontageM * 3.28084
		}
	}
	return values, nil
}

func (s *APIServer) GetDepthFtByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetDepthFtByFeatureIdRequest]) (*connect.Response[parcelsv1.GetDepthFtByFeatureIdResponse], error) {
	values, err := s.getDepthFtByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetDepthFtByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getDepthFtByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]float64, error) {
	values := make(map[int64]float64)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, pa.depth_m
		FROM public.parcel_geometry pg
		JOIN public.parcel_attributes pa ON pg.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var depthM *float64
		if err := rows.Scan(&fid, &depthM); err != nil { return nil, err }
		if depthM != nil {
			values[fid] = *depthM * 3.28084
		}
	}
	return values, nil
}

func (s *APIServer) GetLandUseIdSqftByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetLandUseIdSqftByFeatureIdRequest]) (*connect.Response[parcelsv1.GetLandUseIdSqftByFeatureIdResponse], error) {
	values, err := s.getLandUseIdSqftByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetLandUseIdSqftByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getLandUseIdSqftByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]string, error) {
	values := make(map[int64]string)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, lu.public_id::text
		FROM public.parcel_geometry pg
		JOIN public.parcel_attributes pa ON pg.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		JOIN public.land_uses lu ON pa.land_use_id = lu.land_use_id
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var luId *string
		if err := rows.Scan(&fid, &luId); err != nil { return nil, err }
		if luId != nil {
			values[fid] = *luId
		}
	}
	return values, nil
}

func (s *APIServer) GetZoningIdByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetZoningIdByFeatureIdRequest]) (*connect.Response[parcelsv1.GetZoningIdByFeatureIdResponse], error) {
	values, err := s.getZoningIdByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetZoningIdByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getZoningIdByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]string, error) {
	values := make(map[int64]string)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, (array_remove(array_agg(DISTINCT z.public_id::text), NULL))[1]
		FROM public.parcel_geometry pg
		JOIN public.parcel_affordances pa ON pg.parcel_id = pa.parcel_id AND pa.legal_valid_range @> $1::timestamptz
		JOIN public.zoning z ON pa.zoning_id = z.zoning_id
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz
		GROUP BY pg.feature_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var zoningId *string
		if err := rows.Scan(&fid, &zoningId); err != nil { return nil, err }
		if zoningId != nil {
			values[fid] = *zoningId
		}
	}
	return values, nil
}

func (s *APIServer) GetImprovementAreaSqftByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetImprovementAreaSqftByFeatureIdRequest]) (*connect.Response[parcelsv1.GetImprovementAreaSqftByFeatureIdResponse], error) {
	values, err := s.getImprovementAreaSqftByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetImprovementAreaSqftByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getImprovementAreaSqftByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]float64, error) {
	values := make(map[int64]float64)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, COALESCE(SUM(attr.area_sq_m), 0.0)
		FROM public.parcel_geometry pg
		JOIN public.parcel_improvements pi ON pg.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz AND imp.is_voided = false
		GROUP BY pg.feature_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var areaSqM float64
		if err := rows.Scan(&fid, &areaSqM); err != nil { return nil, err }
		values[fid] = areaSqM * 10.7639
	}
	return values, nil
}

func (s *APIServer) GetBedroomsByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetBedroomsByFeatureIdRequest]) (*connect.Response[parcelsv1.GetBedroomsByFeatureIdResponse], error) {
	values, err := s.getBedroomsByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetBedroomsByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getBedroomsByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]int32, error) {
	values := make(map[int64]int32)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, COALESCE(SUM(attr.bedrooms), 0)::int
		FROM public.parcel_geometry pg
		JOIN public.parcel_improvements pi ON pg.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz AND imp.is_voided = false
		GROUP BY pg.feature_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var bedrooms int32
		if err := rows.Scan(&fid, &bedrooms); err != nil { return nil, err }
		values[fid] = bedrooms
	}
	return values, nil
}

func (s *APIServer) GetBathroomsByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetBathroomsByFeatureIdRequest]) (*connect.Response[parcelsv1.GetBathroomsByFeatureIdResponse], error) {
	values, err := s.getBathroomsByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetBathroomsByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getBathroomsByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]int32, error) {
	values := make(map[int64]int32)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, COALESCE(SUM(attr.bathrooms), 0)::int
		FROM public.parcel_geometry pg
		JOIN public.parcel_improvements pi ON pg.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz AND imp.is_voided = false
		GROUP BY pg.feature_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var bathrooms int32
		if err := rows.Scan(&fid, &bathrooms); err != nil { return nil, err }
		values[fid] = bathrooms
	}
	return values, nil
}

func (s *APIServer) GetUnitsByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetUnitsByFeatureIdRequest]) (*connect.Response[parcelsv1.GetUnitsByFeatureIdResponse], error) {
	values, err := s.getUnitsByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetUnitsByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getUnitsByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]int32, error) {
	values := make(map[int64]int32)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, COALESCE(SUM(attr.units), 0)::int
		FROM public.parcel_geometry pg
		JOIN public.parcel_improvements pi ON pg.parcel_id = pi.parcel_id AND pi.legal_valid_range @> $1::timestamptz
		JOIN public.improvements imp ON pi.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE pg.feature_id = ANY($2::bigint[]) AND pg.legal_valid_range @> $1::timestamptz AND imp.is_voided = false
		GROUP BY pg.feature_id
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var units int32
		if err := rows.Scan(&fid, &units); err != nil { return nil, err }
		values[fid] = units
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementYearBuiltByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementYearBuiltByFeatureIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementYearBuiltByFeatureIdResponse], error) {
	values, err := s.getPrimaryImprovementYearBuiltByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementYearBuiltByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementYearBuiltByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]int32, error) {
	values := make(map[int64]int32)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, attr.year_built
		FROM public.parcel_geometry pg
		JOIN public.get_primary_improvements(ARRAY(
			SELECT pg2.parcel_id 
			FROM public.parcel_geometry pg2 
			WHERE pg2.feature_id = ANY($2::bigint[])
				AND pg2.legal_valid_range @> $1::timestamptz
		), $1::timestamptz) pimp ON pg.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE imp.is_voided = false AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var yearBuilt *int32
		if err := rows.Scan(&fid, &yearBuilt); err != nil { return nil, err }
		if yearBuilt != nil {
			values[fid] = *yearBuilt
		}
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementEffectiveYearBuiltByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementEffectiveYearBuiltByFeatureIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementEffectiveYearBuiltByFeatureIdResponse], error) {
	values, err := s.getPrimaryImprovementEffectiveYearBuiltByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementEffectiveYearBuiltByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementEffectiveYearBuiltByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]int32, error) {
	values := make(map[int64]int32)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, attr.effective_year_built
		FROM public.parcel_geometry pg
		JOIN public.get_primary_improvements(ARRAY(
			SELECT pg2.parcel_id 
			FROM public.parcel_geometry pg2 
			WHERE pg2.feature_id = ANY($2::bigint[])
				AND pg2.legal_valid_range @> $1::timestamptz
		), $1::timestamptz) pimp ON pg.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		WHERE imp.is_voided = false AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var effYearBuilt *int32
		if err := rows.Scan(&fid, &effYearBuilt); err != nil { return nil, err }
		if effYearBuilt != nil {
			values[fid] = *effYearBuilt
		}
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementConditionIdByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementConditionIdByFeatureIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementConditionIdByFeatureIdResponse], error) {
	values, err := s.getPrimaryImprovementConditionIdByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementConditionIdByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementConditionIdByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]string, error) {
	values := make(map[int64]string)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, cond.public_id::text
		FROM public.parcel_geometry pg
		JOIN public.get_primary_improvements(ARRAY(
			SELECT pg2.parcel_id 
			FROM public.parcel_geometry pg2 
			WHERE pg2.feature_id = ANY($2::bigint[])
				AND pg2.legal_valid_range @> $1::timestamptz
		), $1::timestamptz) pimp ON pg.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id AND attr.legal_valid_range @> $1::timestamptz
		LEFT JOIN public.improvement_conditions cond ON attr.improvement_condition_id = cond.improvement_condition_id AND cond.is_voided = false
		WHERE imp.is_voided = false AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var condId *string
		if err := rows.Scan(&fid, &condId); err != nil { return nil, err }
		if condId != nil {
			values[fid] = *condId
		}
	}
	return values, nil
}

func (s *APIServer) GetPrimaryImprovementTypeIdByFeatureId(ctx context.Context, req *connect.Request[parcelsv1.GetPrimaryImprovementTypeIdByFeatureIdRequest]) (*connect.Response[parcelsv1.GetPrimaryImprovementTypeIdByFeatureIdResponse], error) {
	values, err := s.getPrimaryImprovementTypeIdByFeatureId(ctx, req.Msg.FeatureIds, req.Msg.GetLegalAsOf())
	if err != nil { return nil, err }
	return connect.NewResponse(&parcelsv1.GetPrimaryImprovementTypeIdByFeatureIdResponse{Values: values}), nil
}

func (s *APIServer) getPrimaryImprovementTypeIdByFeatureId(ctx context.Context, featureIds []int64, legalAsOf *timestamppb.Timestamp) (map[int64]string, error) {
	values := make(map[int64]string)
	if len(featureIds) == 0 {
		return values, nil
	}
	targetTime := getTargetTime(legalAsOf)
	query := `
		SELECT pg.feature_id, imptype.public_id::text
		FROM public.parcels p
		JOIN public.parcel_geometry pg ON p.parcel_id = pg.parcel_id AND pg.legal_valid_range @> $1::timestamptz
		JOIN public.get_primary_improvements(ARRAY(
			SELECT pg2.parcel_id 
			FROM public.parcel_geometry pg2 
			WHERE pg2.feature_id = ANY($2::bigint[])
				AND pg2.legal_valid_range @> $1::timestamptz
		), $1::timestamptz) pimp ON pg.parcel_id = pimp.parcel_id
		JOIN public.improvements imp ON pimp.improvement_id = imp.improvement_id
		LEFT JOIN public.improvement_attributes attr ON imp.improvement_id = attr.improvement_id 
			AND attr.legal_valid_range @> $1::timestamptz
		LEFT JOIN public.improvement_types imptype ON attr.improvement_type_id = imptype.improvement_type_id
		WHERE imp.is_voided = false AND pg.legal_valid_range @> $1::timestamptz
	`
	rows, err := s.db.Query(ctx, query, targetTime, featureIds)
	if err != nil { return nil, err }
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var typeId *string
		if err := rows.Scan(&fid, &typeId); err != nil { return nil, err }
		if typeId != nil {
			values[fid] = *typeId
		}
	}
	return values, nil
}
