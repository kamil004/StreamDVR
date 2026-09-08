using System.Text.Json;

namespace StreamDVR.Core;

/// <summary>
/// Plain-file settings store (JSON in %APPDATA%\StreamDVR\settings.json).
/// Replaces the macOS Keychain approach.
/// </summary>
public static class ConfigStore
{
    private static readonly string DirectoryPath = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData),
        "StreamDVR");

    private static readonly string FilePath = Path.Combine(DirectoryPath, "settings.json");

    private static Dictionary<string, string> LoadDict()
    {
        try
        {
            if (!File.Exists(FilePath)) return new Dictionary<string, string>();
            var json = File.ReadAllText(FilePath);
            return JsonSerializer.Deserialize<Dictionary<string, string>>(json) ?? new Dictionary<string, string>();
        }
        catch
        {
            return new Dictionary<string, string>();
        }
    }

    private static void SaveDict(Dictionary<string, string> dict)
    {
        try
        {
            Directory.CreateDirectory(DirectoryPath);
            var json = JsonSerializer.Serialize(dict, new JsonSerializerOptions { WriteIndented = true });
            File.WriteAllText(FilePath, json);
        }
        catch
        {
        }
    }

    public static string? Load(string key)
    {
        var dict = LoadDict();
        return dict.TryGetValue(key, out var value) ? value : null;
    }

    public static void Save(string key, string value)
    {
        var dict = LoadDict();
        dict[key] = value;
        SaveDict(dict);
    }

    public static void Delete(string key)
    {
        var dict = LoadDict();
        dict.Remove(key);
        SaveDict(dict);
    }
}