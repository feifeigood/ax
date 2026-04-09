# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""
Example Colab agent for AX: Function Plotter.

Given a mathematical expression, this agent evaluates it over a range
and generates a plot using matplotlib. It demonstrates a Colab agent
that requires external packages (numpy, matplotlib) and produces a
graph saved to the Colab VM.

Supported functions: all numpy functions (sin, cos, exp, log, sqrt,
abs, pi, e, etc.) and standard operators (+, -, *, /, **, etc.).
The variable is `x`.

Usage (standalone):
    pip install numpy matplotlib
    python plot.py --input "sin(x) * exp(-x/10)"

Usage (via AX):
    Configure in ax.yaml:

        registry:
          colab_agents:
            - id: "plotter"
              name: "Function Plotter"
              description: "Plots mathematical functions."
              local_file: "./examples/colab_agent/plot.py"
              requirements: "./examples/colab_agent/requirements.txt"
              input_flag: "input"
              output_image: "./examples/colab_agent/plot.png"

    Then run:
        ax exec --agent plotter --input "sin(x) * exp(-x/10)"
"""

import argparse

import time

import matplotlib
import numpy as np

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402


def plot(expression: str, output_path: str = "./plot.png") -> None:
    """Evaluate a math expression over x and save a plot."""
    print(f"Expression: y = {expression}")
    print()

    # Step 1: Generate x values.
    print("[1/4] Generating sample points...")
    time.sleep(1)
    x = np.linspace(-10, 10, 2000)
    print(f"  x range: [-10, 10], 2000 points")

    # Step 2: Evaluate the expression.
    print("[2/4] Evaluating expression...")
    time.sleep(2)
    # Expose all numpy functions (sin, cos, exp, log, sqrt, pi, e, etc.)
    safe_ns = {name: getattr(np, name) for name in dir(np) if not name.startswith("_")}
    safe_ns["x"] = x
    try:
        y = eval(expression, {"__builtins__": {}}, safe_ns)  # noqa: S307
    except Exception as e:
        print(f"  Error: {e}")
        return

    y = np.asarray(y, dtype=float)
    finite = y[np.isfinite(y)]
    if len(finite) == 0:
        print("  Error: expression produced no finite values")
        return
    print(f"  y range: [{finite.min():.4f}, {finite.max():.4f}]")

    # Step 3: Create the plot.
    print("[3/4] Rendering plot...")
    time.sleep(2)
    fig, ax = plt.subplots(figsize=(10, 6))
    ax.plot(x, y, color="#2563eb", linewidth=2)
    ax.set_xlabel("x", fontsize=12)
    ax.set_ylabel("y", fontsize=12)
    ax.set_title(f"y = {expression}", fontsize=14)
    ax.grid(True, alpha=0.3)
    ax.axhline(y=0, color="black", linewidth=0.5)
    ax.axvline(x=0, color="black", linewidth=0.5)

    # Clamp y-axis to avoid extreme values from singularities.
    margin = (finite.max() - finite.min()) * 0.1 or 1.0
    ax.set_ylim(finite.min() - margin, finite.max() + margin)
    print(f"  Plot size: 10x6 inches, 150 dpi")

    # Step 4: Save.
    print("[4/4] Saving...")
    time.sleep(1)
    fig.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close(fig)
    print(f"  Saved to {output_path}")
    print()
    print("Done.")


def main():
    parser = argparse.ArgumentParser(description="AX Colab function plotter")
    parser.add_argument(
        "--input",
        required=True,
        help="Mathematical expression to plot (variable: x)",
    )
    parser.add_argument(
        "--output",
        default="./plot.png",
        help="Path to save the output image (default: ./plot.png)",
    )
    args = parser.parse_args()
    plot(args.input, args.output)


if __name__ == "__main__":
    main()
