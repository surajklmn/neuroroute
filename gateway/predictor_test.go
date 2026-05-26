package main

import "testing"

func TestPredict(t *testing.T) {
	tests := []struct {
		name     string
		features []float64
		expected int
	}{
		{
			name:     "Light Request Profile",
			features: []float64{0.0, 0.0, 100.0, 0.0, 0.0, 0.0}, // query is not heavy, content length > 57.5
			expected: 0,                                         // Class 0 (Light)
		},
		{
			name:     "Heavy Request Profile",
			features: []float64{0.0, 0.0, 10.0, 1.0, 0.0, 0.0}, // query is heavy, content length <= 57.5
			expected: 2,                                         // Class 2 (Heavy)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Predict(tt.features)
			if got != tt.expected {
				t.Errorf("Predict() = %v, expected %v for features: %v", got, tt.expected, tt.features)
			} else {
				t.Logf("✓ Successfully predicted Class %d for %s", got, tt.name)
			}
		})
	}
}
