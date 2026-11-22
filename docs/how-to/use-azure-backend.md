# How to run fileflow against Azure Blob Storage

Set `FILEFLOW_BACKEND=azure`, choose one way to authenticate, and name a container. The full list of variables is in the [configuration reference](../reference/configuration.md#azure-backend).

## Local Azurite (docker compose)

```bash
FILEFLOW_BACKEND=azure docker compose up --build
```

This starts Azurite on `:10000` and fileflow with the well-known Azurite development connection string. That connection string is public and is not a secret.

## With a connection string

```bash
export FILEFLOW_BACKEND=azure
export AZURE_STORAGE_CONNECTION_STRING='DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...;EndpointSuffix=core.windows.net'
export AZURE_STORAGE_CONTAINER=fileflow
./bin/fileflow
```

Use this for quick tests only. Keys in environment variables are easy to leak.

## With an account URL and Azure identity (recommended)

```bash
export FILEFLOW_BACKEND=azure
export AZURE_STORAGE_ACCOUNT_URL=https://<account>.blob.core.windows.net
export AZURE_STORAGE_CONTAINER=fileflow
./bin/fileflow
```

With an account URL, fileflow authenticates with `DefaultAzureCredential`. On AKS that means Workload Identity, and on a developer machine it means your `az login` session. The identity needs the **Storage Blob Data Contributor** role on the container or account.

## Production checklist

- Use a **ZRS** or **GZRS** storage account so data survives the loss of a zone or region.
- Use a **Private Endpoint** so traffic between the gateway and storage stays on the Azure backbone.
- Create the container yourself and leave `AZURE_CREATE_CONTAINER` unset.

## Check that it works

```bash
curl localhost:8080/readyz
curl -T ./somefile localhost:8080/v1/files/test/somefile
```

## Note on metadata names

Azure only allows C#-identifier metadata names, so fileflow stores `client-kind` as `client_kind` and translates it back on read. You don't need to do anything about this. If you look at blobs with Azure tools, expect the underscore form.
