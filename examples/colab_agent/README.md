# Colab Agents

AX supports executing Python scripts and Jupyter notebooks on Google Colab sessions via the [colab CLI](https://github.com/googlecolab/google-colab-cli). Colab agents provision ephemeral sessions with optional GPU/TPU accelerators, run agent code remotely, stream output back in real time, and tear down the session on completion.

## Prerequisites

- The `colab` CLI installed and available in your `PATH`.
- Application Default Credentials (ADC) authenticated.

  ```sh
  gcloud auth application-default login \
      --scopes=openid,https://www.googleapis.com/auth/cloud-platform,https://www.googleapis.com/auth/userinfo.email,https://www.googleapis.com/auth/colaboratory
  ```

## Configuration

Colab agents are configured in `ax.yaml` under `registry.colab_agents`. Two modes are supported:

### Python script (local file)

A local `.py` file is uploaded to the Colab VM and executed via `!python`.

```yaml
registry:
  colab_agents:
    - id: "plotter"
      name: "Function Plotter"
      description: "Plots mathematical functions on a Colab session."
      local_file: "./examples/colab_agent/plot.py"
      accelerator: "tpu-v5e1"
      requirements: "./examples/colab_agent/requirements.txt"
      input_flag: "input"          # passed as --input to the script
      output_image: "./plot.png"   # downloaded from /content/plot.png on the VM
      output_drive_path: "MyDrive/notebooks/plot.ipynb"  # .py converted to .ipynb, saved to Drive
```

### Jupyter notebook (file on Google Drive)

A notebook on Google Drive is executed via `%run` after mounting Drive.
The input is set as a Python variable in the kernel before the notebook runs.

```yaml
registry:
  colab_agents:
    - id: "data-analysis"
      name: "Data Analysis"
      description: "Analyzes data using a Colab notebook on Google Drive."
      drive_file: "MyDrive/notebooks/data_analysis.ipynb"  # Drive-relative path
      input_flag: "query"          # set as: query = '<user input>'
      output_image: "./chart.png"
```

### Configuration reference

| Field | Required | Description |
|-------|----------|-------------|
| `id` | Yes | Unique agent identifier. |
| `name` | Yes | Human-readable name shown in the planner. |
| `description` | Yes | Description of the agent's capabilities (used by the planner to select agents). |
| `local_file` | One of `local_file` or `drive_file` | Path to a local `.py` or `.ipynb` file on your machine. Uploaded to `/content/` on the VM. |
| `drive_file` | One of `local_file` or `drive_file` | Drive-relative path to a file on Google Drive (e.g. `MyDrive/notebooks/nb.ipynb`). Requires `drive_mount_path`. |
| `accelerator` | No | Hardware accelerator, e.g. `tpu-v5e1` or `gpu-A100`. |
| `drive_mount_path` | No | Path to mount Google Drive on the VM. Defaults to the Colab CLI's standard path (`/content/drive`). Only needed if you want a non-standard mount point. Drive is mounted automatically when `drive_file` or `output_drive_path` is used. Prompts for OAuth authorization on first use. |
| `requirements` | No | Path to a local `requirements.txt`. Packages are installed on the VM before execution. |
| `input_flag` | No | Name of the input parameter. For `.py` files, passed as `--<name>`. For `.ipynb` files, set as a Python variable before `%run`. |
| `output_image` | No | Local path to download an output image to. The remote path is `/content/` + basename (e.g. `./plot.png` downloads from `/content/plot.png`). |
| `output_drive_path` | No | Drive-relative path to save the script converted to a `.ipynb` notebook (e.g. `MyDrive/notebooks/out.ipynb`). The `.py` source is placed in a single code cell. Only supported with `local_file`. |
| `metadata` | No | Optional key-value metadata. |

## Execution flow

### Python scripts (.py)

```
1. colab new -s <session> [-tpu|-gpu <type>]
2. colab drivemount -s <session> <path>              (if drive_mount_path set)
3. colab install -s <session> -r <requirements.txt>  (if requirements set)
4. colab upload <local_file> /content/<basename>
5. echo "!python -u /content/<file> --<flag> '<input>'" | colab exec -s <session>
6. colab download /content/<image> <output_image>    (if output_image set)
7. colab exec: convert .py to .ipynb, save to Drive  (if output_drive_path set)
8. colab stop -s <session>
```

### Jupyter notebooks (.ipynb)

```
1. colab new -s <session> [-tpu|-gpu <type>]
2. colab drivemount -s <session> <path>              (if drive_mount_path set)
3. colab install -s <session> -r <requirements.txt>  (if requirements set)
4. colab upload <local_file> /content/<basename>     (skipped for drive_file)
5. echo "<flag> = '<input>'" | colab exec -s <session>
6. echo "%run <path>" | colab exec -s <session>
7. colab download /content/<image> <output_image>    (if output_image set)
8. colab stop -s <session>
```

### Session timeout retry

If a Colab session is terminated due to idle timeout (e.g. while waiting for Drive authorization), AX automatically recreates the session and retries once.

## Examples

AX includes two examples in `examples/colab_agent/`:

### Function plotter (Python script)

`examples/colab_agent/plot.py` plots mathematical expressions using numpy and matplotlib.

```bash
ax exec --agent plotter --input "sin(x) * exp(-x/10)"
```

### Data analysis (Jupyter notebook)

`examples/colab_agent/data_analysis.ipynb` generates synthetic revenue data and produces a chart.

```bash
ax exec --agent data-analysis --input "Show monthly revenue trend for 2024"
```

## Writing a Colab agent

### Python script

Create a `.py` file that accepts input via `argparse`:

```python
import argparse

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", default="./plot.png")
    args = parser.parse_args()

    # Your agent logic here.
    print(f"Processing: {args.input}")

if __name__ == "__main__":
    main()
```

AX runs scripts with `python -u` (unbuffered stdout).

### Jupyter notebook

Create an `.ipynb` notebook that reads the input variable:

```python
# The AX colab agent sets this variable before %run.
# Fall back to a default for standalone use.
try:
    input
except NameError:
    input = "default query"

# Your notebook logic here.
print(f"Processing: {input}")
```

Note: Notebooks run via `%run` in the IPython kernel, not as a subprocess with `-u`. If you need real-time streaming from a notebook, use `flush=True` on `print()` calls.
