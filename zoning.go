package main

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	zoningv1 "github.com/civil-labs/civil-api-go/civil/mesh/zoning/v1"
)

func (s *APIServer) GetZoning(
	ctx context.Context,
	req *connect.Request[zoningv1.GetZoningRequest],
) (*connect.Response[zoningv1.GetZoningResponse], error) {
	s.logger.Debug("received GetZoning request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			z.public_id::text, 
			a.name, 
			a.code, 
			COALESCE(a.max_far::float4, 0.0), 
			COALESCE(a.min_lot_size_sq_m::float4, 0.0), 
			COALESCE(a.max_height_m::float4, 0.0), 
			COALESCE(a.max_dwelling_units_per_hectare::float4, 0.0), 
			COALESCE(a.max_lot_coverage_pct::float4, 0.0)
		FROM zoning z
		JOIN zoning_attributes a ON z.zoning_id = a.zoning_id
		WHERE NOT z.is_voided
		  AND CURRENT_TIMESTAMP <@ a.legal_valid_range
	`)
	if err != nil {
		s.logger.Error("failed to query zoning", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	zoningMap := make(map[string]*zoningv1.Zoning)
	for rows.Next() {
		var (
			publicID, name, code                                                       string
			maxFar, minLotSizeSqM, maxHeightM, maxDwellingUnitsPerHect, maxCoveragePct float32
		)
		err := rows.Scan(
			&publicID,
			&name,
			&code,
			&maxFar,
			&minLotSizeSqM,
			&maxHeightM,
			&maxDwellingUnitsPerHect,
			&maxCoveragePct,
		)
		if err != nil {
			s.logger.Error("failed to scan zoning row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}

		// Unit conversions: database (metric) to proto (imperial)
		minLotSizeSqFt := minLotSizeSqM * 10.7639
		maxHeightFt := maxHeightM * 3.28084
		maxDwellingUnitsPerAcre := maxDwellingUnitsPerHect * 0.404686

		zoningMap[publicID] = &zoningv1.Zoning{
			Id:                      publicID,
			Name:                    name,
			Code:                    code,
			MaxFar:                  maxFar,
			MinLotSizeSqFt:          minLotSizeSqFt,
			MaxHeightFt:             maxHeightFt,
			MaxDwellingUnitsPerAcre: maxDwellingUnitsPerAcre,
			MaxLotCoveragePct:       maxCoveragePct,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&zoningv1.GetZoningResponse{
		Zoning: zoningMap,
	}), nil
}
