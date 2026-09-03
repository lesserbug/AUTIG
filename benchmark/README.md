# AUTIG Fabric benchmark

The benchmark parameters are defined directly in `fabfile.py`. AWS, SSH,
repository, and instance settings are defined in `settings.json`.

Install the controller dependencies and list tasks:

```bash
cd benchmark
python -m pip install -r requirements.txt
fab --list
```

Run and parse a local benchmark:

```bash
fab local
fab logs
```

For AWS, first set `repo.url` and the SSH key fields in `settings.json`.
The default deployment uses one AUTIG replica per AWS instance and TCP port
`settings.port` on every instance.

AWS credentials are read by `boto3` from the normal environment or AWS
configuration files. Every configured region must contain the named EC2 key
pair, and the instances need public IPv4 addresses because the default region
matrix communicates across regions.

```bash
fab create --nodes=1
fab info
fab install
fab remote
fab logs
fab kill
fab stop
```

`fab create --nodes=1` creates one instance in each configured region. The
`nodes` matrix in `fabfile.py` is the AUTIG replica count, so enough running
instances must exist for the largest configured value. All `n` replicas are
started; `faults` marks the last `f` replicas as the benchmark's malicious
LocalOrder producers and does not mean that those instances are omitted.

Logs are stored in `benchmark/logs` and parsed JSON results in
`benchmark/results`. `fab destroy` permanently terminates all AWS instances
tagged with the default testbed name `autig`.
